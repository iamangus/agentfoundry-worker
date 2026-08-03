package orchestrator

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	_ "image/gif"
	_ "image/png"
	"log/slog"

	"golang.org/x/image/draw"
)

// maxImageEdge is the longest edge (in pixels) that a screenshot returned by
// an MCP tool is allowed to have once it reaches workflow history. Screenshots
// are far too large to keep at full resolution in Temporal history — a single
// 1920x1080 PNG can be multiple MB, and with a handful of tool calls the
// history blows past the 8MB/16MB blob size limits, causing the workflow task
// to time out or fail. Downscaling to a fixed edge keeps history bounded while
// remaining fully readable by the vision model.
const maxImageEdge = 1024

// jpegQuality is used when re-encoding downscaled images. Screenshots are
// photographic enough that quality 82 gives a good size/legibility trade-off.
const jpegQuality = 82

// CompressContentBlocks rewrites any image content blocks to a bounded size.
// Images larger than maxImageEdge are downscaled (aspect ratio preserved) and
// re-encoded as JPEG. Images already within bounds are left untouched so we
// don't destroy pixel-perfect small crops.
func CompressContentBlocks(blocks []ContentBlock) []ContentBlock {
	out := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "image" && b.Data != "" {
			if newData, mime, ok := compressImageData(b.Data, b.MIMEType); ok {
				b.Data = newData
				b.MIMEType = mime
			}
		}
		out = append(out, b)
	}
	return out
}

func compressImageData(data, mime string) (string, string, bool) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", "", false
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", "", false
	}

	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return "", "", false
	}

	longest := w
	if h > longest {
		longest = h
	}
	if longest <= maxImageEdge {
		return "", "", false
	}

	scale := float64(maxImageEdge) / float64(longest)
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return "", "", false
	}

	compressed := base64.StdEncoding.EncodeToString(buf.Bytes())
	slog.Debug("compressed MCP image for workflow history",
		"orig_w", w, "orig_h", h, "new_w", nw, "new_h", nh,
		"orig_len", len(raw), "compressed_len", len(buf.Bytes()),
	)
	return compressed, "image/jpeg", true
}
