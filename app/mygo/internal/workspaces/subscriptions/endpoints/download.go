package endpoints

import (
	"bytes"
	"context"
	"errors"

	"github.com/getkiko/kiko/app/mygo/internal/api"
)

// downloadLimit is the largest received file the console downloads to
// save: the desktop's save dialog takes the file in memory.
const downloadLimit = 256 << 20

var errTooLarge = errors.New("文件超过 256 MiB，请在 Taildrop 接收目录中直接打开")

// limitWriter fails once more than limit bytes are written.
type limitWriter struct {
	buf   bytes.Buffer
	limit int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len()+len(p)) > w.limit {
		return 0, errTooLarge
	}
	return w.buf.Write(p)
}

// downloadReceived reads a received file into memory, reporting the bytes
// so far to progress; a file known to exceed the limit fails before any
// request. Cancel ctx to stop it.
func downloadReceived(ctx context.Context, client *api.Client, tag, name string, size int64, limit int64, progress func(int64)) ([]byte, error) {
	if size > limit {
		return nil, errTooLarge
	}
	w := &limitWriter{limit: limit}
	if err := client.TaildropDownload(ctx, tag, name, w, progress); err != nil {
		if errors.Is(err, errTooLarge) {
			return nil, errTooLarge
		}
		return nil, err
	}
	return w.buf.Bytes(), nil
}
