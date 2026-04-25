package arr

// Field is the *arr config-field struct used in DownloadClient, Notification,
// and Application resources. Value is left as any because *arr uses string,
// int, bool, and []HeaderField depending on the field.
type Field struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// Fields is a named slice type so helpers can hang off it.
type Fields []Field

// Get returns the value of the named field (zero value + false if absent).
func (fs Fields) Get(name string) (any, bool) {
	for i := range fs {
		if fs[i].Name == name {
			return fs[i].Value, true
		}
	}
	return nil, false
}

// Set mutates the field with the matching name to v. Returns true if found.
func (fs Fields) Set(name string, v any) bool {
	for i := range fs {
		if fs[i].Name == name {
			fs[i].Value = v
			return true
		}
	}
	return false
}

// DownloadClientResource matches the GET/POST/PUT shape for
// /api/v3/downloadclient on Sonarr/Radarr.
type DownloadClientResource struct {
	ID             int    `json:"id,omitempty"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	ConfigContract string `json:"configContract"`
	Protocol       string `json:"protocol"`
	Enable         bool   `json:"enable"`
	Priority       int    `json:"priority"`
	Fields         Fields `json:"fields"`
}

// NotificationResource matches /api/v3/notification.
type NotificationResource struct {
	ID                  int    `json:"id,omitempty"`
	Name                string `json:"name"`
	Implementation      string `json:"implementation"`
	ConfigContract      string `json:"configContract"`
	Fields              Fields `json:"fields"`
	OnGrab              bool   `json:"onGrab"`
	OnDownload          bool   `json:"onDownload"`
	OnUpgrade           bool   `json:"onUpgrade"`
	OnHealthIssue       bool   `json:"onHealthIssue"`
	OnApplicationUpdate bool   `json:"onApplicationUpdate"`
}

// HeaderField is the inner header shape used by the notification "headers"
// Field's Value when the implementation is Webhook.
type HeaderField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ApplicationResource matches /api/v1/applications on Prowlarr.
type ApplicationResource struct {
	ID             int    `json:"id,omitempty"`
	Name           string `json:"name"`
	Implementation string `json:"implementation"`
	ConfigContract string `json:"configContract"`
	SyncLevel      string `json:"syncLevel"`
	Fields         Fields `json:"fields"`
}

// ReleaseProfileResource matches /api/v3/releaseprofile on Sonarr/Radarr.
type ReleaseProfileResource struct {
	ID        int      `json:"id,omitempty"`
	Name      string   `json:"name"`
	Enabled   bool     `json:"enabled"`
	Required  []string `json:"required"`
	Ignored   []string `json:"ignored"`
	IndexerID int      `json:"indexerId"`
	Tags      []int    `json:"tags"`
}
