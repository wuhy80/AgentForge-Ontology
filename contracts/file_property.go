package contracts

import (
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// FilePropertyKind distinguishes general attachments from raster images.
type FilePropertyKind string

const (
	FilePropertyAttachment     FilePropertyKind = "attachment"
	FilePropertyImage          FilePropertyKind = "image"
	FilePropertyAttachmentList FilePropertyKind = "attachment-list"
	FilePropertyImageList      FilePropertyKind = "image-list"
)

func (kind FilePropertyKind) Multiple() bool {
	return kind == FilePropertyAttachmentList || kind == FilePropertyImageList
}

func (kind FilePropertyKind) Image() bool {
	return kind == FilePropertyImage || kind == FilePropertyImageList
}

func (kind FilePropertyKind) Valid() bool {
	return kind == FilePropertyAttachment || kind == FilePropertyImage || kind.Multiple()
}

// FileSummary is the public metadata for a file bound to an instance field.
type FileSummary struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	Size      int64  `json:"size"`
}

// DescribePropertyFile derives metadata from bounded upload bytes. The result
// identifies the detected content type; image decoding and download disposition
// remain the responsibility of the service that accepts or serves the file.
func DescribePropertyFile(kind FilePropertyKind, name string, content []byte, maxBytes int64) (FileSummary, error) {
	if !kind.Valid() {
		return FileSummary{}, errors.New("文件属性类型无效")
	}
	if !utf8.ValidString(name) || len(name) > 255 || name != strings.TrimSpace(name) ||
		name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) ||
		strings.IndexFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return FileSummary{}, errors.New("文件名称无效")
	}
	size := int64(len(content))
	if size == 0 || size > maxBytes {
		return FileSummary{}, errors.New("文件大小超出限制")
	}
	mediaType := http.DetectContentType(content)
	if kind.Image() {
		switch mediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
		default:
			return FileSummary{}, errors.New("不支持此图片格式")
		}
	}
	return FileSummary{Name: name, MediaType: mediaType, Size: size}, nil
}
