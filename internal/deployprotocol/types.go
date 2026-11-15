// Package deployprotocol defines the narrow local protocol between Auth and its
// deployment agent. It deliberately has no command, path, URL or credential field.
package deployprotocol

type Release struct {
	Version  string `json:"version"`
	Schema   int    `json:"schema"`
	Platform string `json:"platform"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	AssetID  int64  `json:"asset_id"`
}

type Image struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Schema  int    `json:"schema"`
	Health  string `json:"health"`
}

type Request struct {
	Action             string `json:"action"`
	Version            string `json:"version"`
	SHA256             string `json:"sha256"`
	ExpectedImage      string `json:"expected_image"`
	TargetImage        string `json:"target_image"`
	IdempotencyKey     string `json:"idempotency_key"`
	AllowSchemaUpgrade bool   `json:"allow_schema_upgrade"`
	Actor              string `json:"actor"`
}

type Job struct {
	ID        string   `json:"id"`
	Request   Request  `json:"request"`
	Release   *Release `json:"release,omitempty"`
	Status    string   `json:"status"`
	Stage     string   `json:"stage"`
	Message   string   `json:"message"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
}

type Status struct {
	Enabled         bool     `json:"enabled"`
	Connected       bool     `json:"connected"`
	Repository      string   `json:"repository"`
	Platform        string   `json:"platform"`
	Current         *Image   `json:"current"`
	Previous        *Image   `json:"previous"`
	Managed         bool     `json:"managed"`
	Pending         bool     `json:"pending"`
	Locked          bool     `json:"locked"`
	RollbackAllowed bool     `json:"rollback_allowed"`
	UpdateAvailable bool     `json:"update_available"`
	Release         *Release `json:"release"`
	Job             *Job     `json:"job"`
	Message         string   `json:"message"`
}

type Manifest struct {
	Format   int    `json:"format"`
	Version  string `json:"version"`
	Schema   int    `json:"schema"`
	Platform string `json:"platform"`
	Bundle   string `json:"bundle"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}
