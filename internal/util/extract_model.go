package util

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

const maxPooledBufferCap = 64 * 1024

var bodyBufPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

func ExtractModelFromRequest(r *http.Request, maxModelScanBytes int64) string {
	if r == nil || r.URL == nil {
		return ""
	}
	// 1. Gemini: 从 URL 拿
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		prefix := "/v1beta/models/"
		rest := strings.TrimPrefix(r.URL.Path, prefix)
		parts := strings.SplitN(rest, ":", 2)
		return parts[0]
	}

	// 2. 自定义 header（如果客户端愿意传）
	if model := r.Header.Get("X-Model-Name"); model != "" {
		return model
	}

	// 3. POST JSON body 里拿
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/") {
		return extractModelFromJSONBody(r, maxModelScanBytes)
	}
	return ""
}

func extractModelFromJSONBody(r *http.Request, maxModelScanBytes int64) string {
	origBody := r.Body

	if r.Body == nil {
		return ""
	}

	buf := bodyBufPool.Get().(*bytes.Buffer)
	buf.Reset()

	// TeeReader：decoder 读了多少，就同步缓存多少
	// LimitReader：最多只扫描 maxModelScanBytes，避免大 body 占用内存

	var limitedReader io.Reader

	// 关键：0 = 不限制
	if maxModelScanBytes > 0 {
		limitedReader = io.LimitReader(io.TeeReader(origBody, buf), maxModelScanBytes)
	} else {
		limitedReader = io.TeeReader(origBody, buf)
	}

	dec := json.NewDecoder(limitedReader)

	// 无论是否成功，都要把 body 还原，保证后续 handler 还能正常读取
	defer func() {
		// 注意：
		// json.Decoder 可能会预读一部分数据。
		// 但所有从 origBody 读走的数据都会经过 TeeReader 写入 buf，
		// 所以用 buf + origBody 可以恢复完整 body。
		r.Body = &pooledRestoreBody{
			Reader: io.MultiReader(
				bytes.NewReader(buf.Bytes()),
				origBody,
			),
			orig: origBody,
			buf:  buf,
		}
	}()

	model, _ := scanTopLevelModel(dec)
	return model
}

func scanTopLevelModel(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}

	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return "", nil
	}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return "", err
		}

		key, ok := keyTok.(string)
		if !ok {
			return "", nil
		}

		if key == "model" {
			valueTok, err := dec.Token()
			if err != nil {
				return "", err
			}

			if model, ok := valueTok.(string); ok {
				return model, nil
			}

			return "", nil
		}

		// 跳过非 model 字段的值
		if err := skipJSONValue(dec); err != nil {
			return "", err
		}
	}

	return "", nil
}

func skipJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}

	delim, ok := tok.(json.Delim)
	if !ok {
		// 普通 primitive 值：string / number / bool / null
		return nil
	}

	switch delim {
	case '{', '[':
		depth := 1
		for depth > 0 {
			tok, err := dec.Token()
			if err != nil {
				return err
			}

			if d, ok := tok.(json.Delim); ok {
				switch d {
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
		}
	}

	return nil
}

type pooledRestoreBody struct {
	io.Reader

	orig io.Closer
	buf  *bytes.Buffer

	once     sync.Once
	closeErr error
}

func (b *pooledRestoreBody) Close() error {
	b.once.Do(func() {
		if b.orig != nil {
			if err := b.orig.Close(); err != nil {
				b.closeErr = err
			}
			b.orig = nil
		}

		if b.buf != nil {
			if b.buf.Cap() <= maxPooledBufferCap {
				b.buf.Reset()
				bodyBufPool.Put(b.buf)
			}
			b.buf = nil
		}
	})

	return b.closeErr
}
