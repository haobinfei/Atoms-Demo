package artifact

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// COSConfig configures Tencent Cloud COS through its S3-compatible API.
type COSConfig struct {
	Bucket       string
	Endpoint     string
	Region       string
	SecretID     string
	SecretKey    string
	SessionToken string
}

type cosBackend struct {
	client *s3.Client
	bucket string
}

// NewCOS creates an artifact store backed by Tencent Cloud COS.
func NewCOS(cfg COSConfig) (*Store, error) {
	backend, err := newCOSBackend(cfg)
	if err != nil {
		return nil, err
	}
	return New(backend), nil
}

func newCOSBackend(cfg COSConfig) (*cosBackend, error) {
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	cfg.Endpoint = strings.TrimSpace(cfg.Endpoint)
	cfg.SecretID = strings.TrimSpace(cfg.SecretID)
	cfg.SecretKey = strings.TrimSpace(cfg.SecretKey)
	cfg.SessionToken = strings.TrimSpace(cfg.SessionToken)
	if cfg.Bucket == "" || cfg.Endpoint == "" || cfg.SecretID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("COS_BUCKET, COS_ENDPOINT, COS_SECRET_ID and COS_SECRET_KEY are required together")
	}
	endpoint := cfg.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid COS_ENDPOINT %q", cfg.Endpoint)
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = regionFromHost(u.Hostname())
	}
	if region == "" {
		region = "ap-beijing"
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(strings.TrimRight(endpoint, "/")),
		Region:       region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(
			cfg.SecretID, cfg.SecretKey, cfg.SessionToken,
		)),
		UsePathStyle: true,
		HTTPClient:   &http.Client{Timeout: 2 * time.Minute},
		APIOptions: []func(*middleware.Stack) error{
			addCOSDeleteObjectsContentMD5,
		},
	})
	return &cosBackend{client: client, bucket: cfg.Bucket}, nil
}

// Tencent COS requires Content-MD5 on DeleteObjects even though current AWS
// SDKs no longer expose that field on the operation input. Add it after the
// XML body has been serialized and before SigV4 signs the request headers.
func addCOSDeleteObjectsContentMD5(stack *middleware.Stack) error {
	return stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc(
		"COSDeleteObjectsContentMD5",
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (
			out middleware.FinalizeOutput, metadata middleware.Metadata, err error,
		) {
			if middleware.GetOperationName(ctx) != "DeleteObjects" {
				return next.HandleFinalize(ctx, in)
			}
			req, ok := in.Request.(*smithyhttp.Request)
			if !ok {
				return out, metadata, fmt.Errorf("COS DeleteObjects request has unexpected type %T", in.Request)
			}
			stream := req.GetStream()
			if stream == nil {
				req.Header.Set("Content-MD5", contentMD5(nil))
				return next.HandleFinalize(ctx, in)
			}
			if !req.IsStreamSeekable() {
				return out, metadata, errors.New("COS DeleteObjects request body is not seekable")
			}
			body, err := io.ReadAll(stream)
			if err != nil {
				return out, metadata, fmt.Errorf("read COS DeleteObjects request body: %w", err)
			}
			if err := req.RewindStream(); err != nil {
				return out, metadata, fmt.Errorf("rewind COS DeleteObjects request body: %w", err)
			}
			req.Header.Set("Content-MD5", contentMD5(body))
			return next.HandleFinalize(ctx, in)
		},
	), "Signing", middleware.Before)
}

func contentMD5(body []byte) string {
	sum := md5.Sum(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func regionFromHost(host string) string {
	parts := strings.Split(host, ".")
	for i, part := range parts {
		if part == "cos" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func (c *cosBackend) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("COS put %s: %w", key, err)
	}
	return nil
}

func (c *cosBackend) Get(ctx context.Context, key string) (Object, error) {
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return Object{}, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return Object{}, fmt.Errorf("COS get %s: %w", key, err)
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	var modTime time.Time
	if out.LastModified != nil {
		modTime = *out.LastModified
	}
	return Object{
		Body:        out.Body,
		Size:        size,
		ModTime:     modTime,
		ETag:        aws.ToString(out.ETag),
		ContentType: aws.ToString(out.ContentType),
	}, nil
}

func (c *cosBackend) Copy(ctx context.Context, srcKey, dstKey string) error {
	_, err := c.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(c.bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(c.copySource(srcKey)),
	})
	if err != nil {
		return fmt.Errorf("COS copy %s -> %s: %w", srcKey, dstKey, err)
	}
	return nil
}

func (c *cosBackend) DeletePrefix(ctx context.Context, prefix string) error {
	pager := s3.NewListObjectsV2Paginator(c.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(prefix),
	})
	batch := make([]types.ObjectIdentifier, 0, 1000)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, err := c.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(c.bucket),
			Delete: &types.Delete{Objects: batch, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return fmt.Errorf("COS delete prefix %s: %w", prefix, err)
		}
		batch = batch[:0]
		return nil
	}
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("COS list prefix %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			// ListObjectsV2 uses a raw byte prefix. Match the Store's
			// namespace semantics instead: the exact key or a child path,
			// never a sibling whose name merely starts with the same text
			// (for example "gemma" must not delete "gemma-2").
			if !matchesObjectPrefix(*obj.Key, prefix) {
				continue
			}
			batch = append(batch, types.ObjectIdentifier{Key: obj.Key})
			if len(batch) == cap(batch) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	return flush()
}

func matchesObjectPrefix(key, prefix string) bool {
	return key == prefix || strings.HasPrefix(key, prefix+"/")
}

func (c *cosBackend) copySource(key string) string {
	parts := strings.Split(key, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return url.PathEscape(c.bucket) + "/" + strings.Join(parts, "/")
}

func isS3NotFound(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket":
			return true
		}
	}
	return false
}
