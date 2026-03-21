package gridappsd

// GridAPPSDConfig mirrors the Python server's gridappsd configuration section.
type GridAPPSDConfig struct {
	// Connection
	Address  string `yaml:"address"`  // STOMP host (default: localhost)
	Port     int    `yaml:"port"`     // STOMP port (default: 61613)
	Username string `yaml:"username"` // STOMP credentials
	Password string `yaml:"password"`

	// CIM-Graph gRPC
	CIMGraphAddr string `yaml:"cimgraph_addr"` // gRPC address (default: localhost:50051)

	// Model
	ModelName string `yaml:"model_name"` // GridAPPS-D model name
	FeederID  string `yaml:"feeder_id"`  // Feeder mRID

	// Device discovery patterns (regex)
	HouseInvertersRegex   string `yaml:"house_named_inverters_regex"`
	UtilityInvertersRegex string `yaml:"utility_named_inverters_regex"`

	// Publishing
	PublishIntervalSeconds int    `yaml:"publish_interval_seconds"` // default: 30
	PublishTopic           string `yaml:"publish_topic"`

	// Service
	ServiceName  string `yaml:"service_name"`   // default: IEEE_2030_5
	SimulationID string `yaml:"simulation_id"`

	// Defaults
	DefaultPIN int `yaml:"default_pin"` // default: 111115
}

// DefaultGridAPPSDConfig returns sensible defaults.
func DefaultGridAPPSDConfig() GridAPPSDConfig {
	return GridAPPSDConfig{
		Address:                "localhost",
		Port:                   61613,
		Username:               "system",
		Password:               "manager",
		CIMGraphAddr:           "localhost:50051",
		PublishIntervalSeconds: 30,
		ServiceName:            "IEEE_2030_5",
		DefaultPIN:            111115,
	}
}
