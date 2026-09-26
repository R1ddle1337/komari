package metric

import "errors"

// WriteAcceptedError reports a persistence failure after WriteBatch has
// accepted every point into its in-memory raw samples and rollups. Callers must
// not replay the input: the store retains the unflushed rollups for Flush or
// Close to retry, even after the raw sample window expires.
type WriteAcceptedError struct {
	Err error
}

func (e *WriteAcceptedError) Error() string {
	return "metric: samples accepted, rollup flush failed: " + e.Err.Error()
}

func (e *WriteAcceptedError) Unwrap() error { return e.Err }

var (
	// ErrInvalidArgument reports an invalid caller-supplied argument.
	//
	// ErrInvalidArgument 表示调用方传入了无效参数。
	ErrInvalidArgument = errors.New("metric: invalid argument")
	// ErrNotFound reports a missing metric or record.
	//
	// ErrNotFound 表示指标或记录不存在。
	ErrNotFound = errors.New("metric: not found")
	// ErrNoData reports that a valid query range contained no samples.
	//
	// ErrNoData 表示有效查询范围内没有采样。
	ErrNoData = errors.New("metric: no data in range")
	// ErrAlreadyExists reports that a create-only operation found an existing row.
	//
	// ErrAlreadyExists 表示只创建操作遇到了已存在的行。
	ErrAlreadyExists = errors.New("metric: already exists")
	// ErrClosed reports that the store has already been closed.
	//
	// ErrClosed 表示 Store 已经关闭。
	ErrClosed = errors.New("metric: store is closed")
)
