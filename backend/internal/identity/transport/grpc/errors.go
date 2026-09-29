package grpc

import (
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error is the sole translation from application/domain failures to gRPC status.
// 【阅读 8】Error 将 invalid 原因转换为参数错误，供网关映射 HTTP 400。
// 【阅读 9】unauthenticated 表示缺失或失效的身份凭据，供网关映射 HTTP 401。
// 【阅读 10】forbidden 表示已识别调用方没有操作权限，供网关映射 HTTP 403。
// 【阅读 11】unavailable 表示当前依赖无法完成操作，供网关映射 HTTP 503。
func Error(err error) error {
	if err == nil {
		return nil
	}
	failure := application.Failure(err)
	code := codes.Unavailable
	switch failure.Kind {
	case application.Invalid:
		code = codes.InvalidArgument
	case application.Unauthenticated:
		code = codes.Unauthenticated
	case application.Forbidden:
		code = codes.PermissionDenied
	case application.NotFound:
		code = codes.NotFound
	case application.AlreadyExists:
		code = codes.AlreadyExists
	case application.Aborted:
		code = codes.Aborted
	}
	return status.Error(code, failure.Reason)
}
