package server

// SensitiveAdminPatterns exposes the certificate and traffic-capture route
// families to the external test package, which holds it equal to every
// "/api/certs" and "/api/traffic" pattern BuildAdminRouter reports (#579,
// #631).
var SensitiveAdminPatterns = sensitiveAdminPatterns

// NonSensitiveAdminWrites exposes the admin write-route allowlist to the
// external test package, which derives the default-protected write class as
// every authed write route not in this set and not already in
// SensitiveAdminPatterns (#579 fix round 2, MEDIUM).
var NonSensitiveAdminWrites = nonSensitiveAdminWrites
