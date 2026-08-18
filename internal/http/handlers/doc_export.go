package handlers

import (
	"fmt"
	"net/http"
	"strings"
)

// docExportFormats maps a ?format= value to its content type and file extension — the single
// definition shared by every document-generating handler in this package (purchase orders,
// transfers, goods receipts, requisitions, RFQs, purchase returns, stock adjustments, stock
// counts, bundle specs). pdf is the default (and the only inline-previewable one — csv/xlsx
// always download, since neither renders in the shared PdfPreview modal the UI opens for pdf).
var docExportFormats = map[string]struct{ mime, ext string }{
	"pdf":  {"application/pdf", "pdf"},
	"csv":  {"text/csv", "csv"},
	"xlsx": {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "xlsx"},
}

// docFormatFromQuery resolves and validates ?format=, defaulting to "pdf". ok is false when the
// caller already wrote a 400 for an unrecognised value.
func docFormatFromQuery(w http.ResponseWriter, r *http.Request) (format string, ok bool) {
	format = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "pdf"
	}
	if _, known := docExportFormats[format]; !known {
		writeError(w, http.StatusBadRequest, "INVALID_FORMAT", `format must be "pdf", "csv", or "xlsx"`)
		return "", false
	}
	return format, true
}

// writeDocFile writes fileBytes with format-appropriate content-type/disposition headers. pdf
// renders inline (unchanged behavior for every existing caller of writePDF below); csv/xlsx
// always download, since neither has an in-browser preview in the UI.
func writeDocFile(w http.ResponseWriter, filename, format string, fileBytes []byte) {
	meta, ok := docExportFormats[format]
	if !ok {
		meta = docExportFormats["pdf"]
		format = "pdf"
	}
	if filename == "" {
		filename = "document"
	}
	disposition := "inline"
	if format != "pdf" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", meta.mime)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s.%s"`, disposition, filename, meta.ext))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(fileBytes)
}

// writePDF writes rendered PDF bytes with the inline-disposition headers every pdf-only document
// endpoint in this service uses. Equivalent to writeDocFile(w, filename, "pdf", body); kept as its
// own name since "write the PDF" reads better at call sites that don't offer a format choice.
func writePDF(w http.ResponseWriter, filename string, body []byte) {
	writeDocFile(w, filename, "pdf", body)
}
