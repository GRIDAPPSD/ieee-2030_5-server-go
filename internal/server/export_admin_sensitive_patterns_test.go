package server

// SensitiveAdminPatterns exposes the certificate and traffic-capture route
// families to the external test package, which holds it equal to every
// "/api/certs" and "/api/traffic" pattern BuildAdminRouter reports (#579,
// #631).
var SensitiveAdminPatterns = sensitiveAdminPatterns
