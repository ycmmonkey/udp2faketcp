package udp2faketcp

import (
	"time"
)

var DEBUG = false
var MAX_PACKET_LEN = 1440
var UDP_TTL = 10 * time.Second
