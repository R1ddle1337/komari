package public

import _ "embed"

//go:embed cache-worker.js
var cacheWorker []byte

//go:embed cache-bootstrap.js
var cacheBootstrap []byte
