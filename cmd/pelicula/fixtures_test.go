package main

import "time"

// fixedTime is an arbitrary past timestamp used to detect unwanted rewrites.
var fixedTime = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
