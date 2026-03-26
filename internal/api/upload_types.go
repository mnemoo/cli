package api

type FileUploadBody struct {
	Team string `json:"team"`
	Game string `json:"game"`
	Path string `json:"path"`
	ETag string `json:"etag,omitempty"`
	Size int64  `json:"size"`
}

type UploadResponse struct {
	Key string `json:"key"`
	URL string `json:"url"`
}

type FilePart struct {
	ETag   string `json:"etag"`
	Number int    `json:"number"`
	Size   int64  `json:"size"`
}

type FileMultiUploadBody struct {
	Filename string     `json:"filename"`
	Team     string     `json:"team"`
	Game     string     `json:"game"`
	Path     string     `json:"path"`
	Size     int64      `json:"size"`
	Parts    []FilePart `json:"parts"`
}

type MultiUploadPartResponse struct {
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Number   int    `json:"number"`
	Complete bool   `json:"complete"`
}

type MultiUploadInitResponse struct {
	Key      string                    `json:"key"`
	UploadID string                    `json:"uploadID"`
	Parts    []MultiUploadPartResponse `json:"parts"`
}

type FileCompletePartBody struct {
	ETag   string `json:"etag"`
	Size   int64  `json:"size"`
	Number int    `json:"number"`
}

type FileCompleteBody struct {
	Team     string                 `json:"team"`
	Game     string                 `json:"game"`
	UploadID string                 `json:"uploadID"`
	Key      string                 `json:"key"`
	Parts    []FileCompletePartBody `json:"parts"`
}

type CompleteResponse struct {
	ETag string `json:"etag"`
}

type CopyResponse struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
}

type FileMulticopyBody struct {
	Team     string     `json:"team"`
	Game     string     `json:"game"`
	PathTo   string     `json:"pathTo"`
	PathFrom string     `json:"pathFrom"`
	ETag     string     `json:"etag"`
	Size     int64      `json:"size"`
	Parts    []FilePart `json:"parts"`
}

type DeleteBody struct {
	Team  string   `json:"team"`
	Game  string   `json:"game"`
	Paths []string `json:"paths"`
}

type DeleteResponse struct {
	Deleted     int      `json:"deleted"`
	FailedPaths []string `json:"failedPaths"`
}

type PublishBody struct {
	Team string `json:"team"`
	Game string `json:"game"`
}

type PublishResponse struct {
	Version int  `json:"version"`
	Changed bool `json:"changed"`
}

type PublishResult struct {
	Version int     `json:"version"`
	Changed bool    `json:"changed"`
	Code    string  `json:"code"`
	Message string  `json:"message"`
	File    *string `json:"file,omitempty"`
	Mode    *string `json:"mode,omitempty"`
}

func (r *PublishResult) IsError() bool {
	return r.Code != ""
}

func (r *PublishResult) Error() string {
	s := r.Message
	if r.File != nil {
		s += " (file: " + *r.File + ")"
	}
	if r.Mode != nil {
		s += " (mode: " + *r.Mode + ")"
	}
	return s
}

type PublishError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	File    *string `json:"file"`
	Mode    *string `json:"mode"`
}

type ScratchObject struct {
	Key  string `json:"key"`
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}
