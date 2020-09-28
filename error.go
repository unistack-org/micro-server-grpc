package grpc

import (
	"context"
	"io"
	"net/http"
	"os"

	pb "github.com/unistack-org/micro-server-grpc/internal/errors"
	"github.com/unistack-org/micro/v3/errors"
	"google.golang.org/grpc/codes"
)

var (
	errMapping = map[int32]codes.Code{
		http.StatusOK:                  codes.OK,
		http.StatusBadRequest:          codes.InvalidArgument,
		http.StatusRequestTimeout:      codes.DeadlineExceeded,
		http.StatusNotFound:            codes.NotFound,
		http.StatusConflict:            codes.AlreadyExists,
		http.StatusForbidden:           codes.PermissionDenied,
		http.StatusUnauthorized:        codes.Unauthenticated,
		http.StatusPreconditionFailed:  codes.FailedPrecondition,
		http.StatusNotImplemented:      codes.Unimplemented,
		http.StatusInternalServerError: codes.Internal,
		http.StatusServiceUnavailable:  codes.Unavailable,
	}
)

// convertCode converts a standard Go error into its canonical code. Note that
// this is only used to translate the error returned by the server applications.
func convertCode(err error) codes.Code {
	switch err {
	case nil:
		return codes.OK
	case io.EOF:
		return codes.OutOfRange
	case io.ErrClosedPipe, io.ErrNoProgress, io.ErrShortBuffer, io.ErrShortWrite, io.ErrUnexpectedEOF:
		return codes.FailedPrecondition
	case os.ErrInvalid:
		return codes.InvalidArgument
	case context.Canceled:
		return codes.Canceled
	case context.DeadlineExceeded:
		return codes.DeadlineExceeded
	}
	switch {
	case os.IsExist(err):
		return codes.AlreadyExists
	case os.IsNotExist(err):
		return codes.NotFound
	case os.IsPermission(err):
		return codes.PermissionDenied
	}
	return codes.Unknown
}

func microError(err error) codes.Code {
	if err == nil {
		return codes.OK
	}

	var ec int32
	switch verr := err.(type) {
	case *errors.Error:
		ec = verr.Code
	case *pb.Error:
		ec = verr.Code
	}

	if code, ok := errMapping[ec]; ok {
		return code
	}

	return codes.Unknown
}

func pbError(err error) *pb.Error {
	switch verr := err.(type) {
	case nil:
		return nil
	case *errors.Error:
		return &pb.Error{Id: verr.Id, Code: verr.Code, Detail: verr.Detail, Status: verr.Status}
	case *pb.Error:
		return verr
	default:
		return &pb.Error{Code: 500, Detail: err.Error()}
	}
}
