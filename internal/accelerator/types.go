package accelerator

import "time"

const (
	protocolVersion          = 2
	relayParallelConnections = 4
)

var speedTestMetadata = []byte(`{"kind":"speedtest","version":1}`)

type taskMetadata struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type TaskStatus string

const (
	TaskReceiving TaskStatus = "receiving"
	TaskQueued    TaskStatus = "queued"
	TaskUploading TaskStatus = "uploading_to_stake"
	TaskDone      TaskStatus = "done"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

type Task struct {
	ID        string     `json:"id"`
	Status    TaskStatus `json:"status"`
	Size      int64      `json:"size"`
	Received  int64      `json:"received"`
	Expected  int64      `json:"expected,omitempty"`
	Uploaded  int64      `json:"uploaded"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	StagedAt  *time.Time `json:"staged_at,omitempty"`
	StartedAt *time.Time `json:"remote_started_at,omitempty"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

type tasksResponse struct {
	Protocol int    `json:"protocol"`
	Tasks    []Task `json:"tasks"`
}

type SpeedTestResult struct {
	Bytes          int64         `json:"bytes"`
	Connections    int           `json:"connections"`
	ClientDuration time.Duration `json:"client_duration"`
	ServerDuration time.Duration `json:"server_duration"`
}

// speedTestLaneResult is returned by one physical relay connection. The
// client combines several lane windows on the server's clock so the reported
// receive/decrypt rate measures the same parallel transport as an upload.
type speedTestLaneResult struct {
	Bytes            int64         `json:"bytes"`
	ServerDuration   time.Duration `json:"server_duration"`
	ServerStartedAt  time.Time     `json:"server_started_at"`
	ServerFinishedAt time.Time     `json:"server_finished_at"`
}
