package artifact

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go/middleware"
)

func TestMatchesObjectPrefix(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		prefix string
		want   bool
	}{
		{name: "exact", key: "sessions/abc", prefix: "sessions/abc", want: true},
		{name: "child", key: "sessions/abc/current.json", prefix: "sessions/abc", want: true},
		{name: "sibling", key: "sessions/abc-2/current.json", prefix: "sessions/abc", want: false},
		{name: "provider sibling", key: "sessions/abc/contest/gemma-2/current.json", prefix: "sessions/abc/contest/gemma", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesObjectPrefix(tt.key, tt.prefix); got != tt.want {
				t.Fatalf("matchesObjectPrefix(%q, %q) = %v, want %v", tt.key, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestDeleteObjectsContentMD5(t *testing.T) {
	var requestBody []byte
	var requestMD5 string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requestBody = body
		requestMD5 = r.Header.Get("Content-MD5")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<DeleteResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></DeleteResult>`)
	}))
	defer srv.Close()

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(srv.URL),
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("test-id", "test-key", ""),
		UsePathStyle: true,
		APIOptions:   []func(*middleware.Stack) error{addCOSDeleteObjectsContentMD5},
	})
	_, err := client.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
		Bucket: aws.String("test-bucket"),
		Delete: &types.Delete{Objects: []types.ObjectIdentifier{
			{Key: aws.String("sessions/abc/current.json")},
		}},
	})
	if err != nil {
		t.Fatalf("DeleteObjects: %v", err)
	}
	if !strings.Contains(string(requestBody), "<Key>sessions/abc/current.json</Key>") {
		t.Fatalf("DeleteObjects body = %q, want serialized key", requestBody)
	}
	wantMD5 := md5.Sum(requestBody)
	want := base64.StdEncoding.EncodeToString(wantMD5[:])
	if requestMD5 != want {
		t.Fatalf("Content-MD5 = %q, want %q for body %q", requestMD5, want, requestBody)
	}
}
