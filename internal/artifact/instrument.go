package artifact

import (
	"context"
	"io"

	"github.com/haobinfei/Atoms-Demo/internal/observability"
)

// instrumentedBackend 是 Backend 的埋点装饰器。产物存储是唯一出网到 COS 的
// 路径，所以「构建上传失败」会先在这里的错误率上显形，然后才变成一个失败的回合
// ——先看见前者，排障才是分钟级而不是小时级。
//
// 装饰在 Backend 这一层而不是 Store 的每个方法上：五种底层操作覆盖了全部调用
// 路径，一处包装即可，不必给每个上层方法都套一层。
type instrumentedBackend struct {
	inner Backend
}

// instrument wraps a Backend with operation counters.
func instrument(inner Backend) Backend {
	return &instrumentedBackend{inner: inner}
}

func (b *instrumentedBackend) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	err := b.inner.Put(ctx, key, body, size, contentType)
	observeArtifact(ctx, "put", err)
	return err
}

func (b *instrumentedBackend) Get(ctx context.Context, key string) (Object, error) {
	obj, err := b.inner.Get(ctx, key)
	observeArtifact(ctx, "get", err)
	return obj, err
}

func (b *instrumentedBackend) Copy(ctx context.Context, srcKey, dstKey string) error {
	err := b.inner.Copy(ctx, srcKey, dstKey)
	observeArtifact(ctx, "copy", err)
	return err
}

func (b *instrumentedBackend) DeletePrefix(ctx context.Context, prefix string) error {
	err := b.inner.DeletePrefix(ctx, prefix)
	observeArtifact(ctx, "delete_prefix", err)
	return err
}

func (b *instrumentedBackend) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	objects, err := b.inner.List(ctx, prefix)
	observeArtifact(ctx, "list", err)
	return objects, err
}

func observeArtifact(ctx context.Context, op string, err error) {
	outcome := observability.OutcomeSuccess
	if err != nil {
		outcome = observability.OutcomeFailed
	}
	observability.Obs().ArtifactOps.Add(ctx, 1, observability.Attrs(
		observability.AttrOp.String(op),
		observability.AttrOutcome.String(outcome),
	))
}
