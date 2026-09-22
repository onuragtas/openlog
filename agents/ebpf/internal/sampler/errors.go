package sampler

import "errors"

var errShortKey = errors.New("sampler: key is shorter than the BPF map's key size")
