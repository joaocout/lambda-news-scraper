package util

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strings"
)

func StringContainsAnyOf(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(strings.ToLower(s), strings.ToLower(t)) {
			return true
		}
	}
	return false
}

func HashAndTruncateBy(s string, t int) (string, error) {
	hasher := md5.New()
	_, err := hasher.Write([]byte(s))

	if err != nil {
		return "", fmt.Errorf("error writing to hasher: %v", err)
	}
	return hex.EncodeToString(hasher.Sum(nil))[:t], nil
}

func MFilter[K comparable, V any](m map[K]V, test func(K, V) (bool, error)) (ret map[K]V, err error) {
	ret = make(map[K]V)
	for k, v := range m {
		if ok, err := test(k, v); err != nil {
			return nil, fmt.Errorf("error in map filter test function: %v", err)
		} else if ok {
			ret[k] = v
		}
	}
	return
}
