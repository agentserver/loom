package commandiface

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type CommandInterface struct {
	Skill   string `json:"skill"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
	Default bool   `json:"default"`
}

type Capabilities struct {
	Platform          Platform           `json:"platform"`
	Skills            []string           `json:"skills"`
	CommandInterfaces []CommandInterface `json:"command_interfaces,omitempty"`
}
