package util

import "strings"

func StringContainsAnyOf(s string, terms []string) bool {
	for _, t := range terms {
		if strings.Contains(strings.ToLower(s), strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// func LFilter[T any](ss []T, test func(T) bool) (ret []T) {
// 	for _, s := range ss {
// 		if test(s) {
// 			ret = append(ret, s)
// 		}
// 	}
// 	return
// }

// func LMap[T any, R any](ss []T, f func(T) R) (ret []R) {
// 	for _, s := range ss {
// 		ret = append(ret, f(s))
// 	}
// 	return
// }

func MFilter[K comparable, V any](m map[K]V, test func(K, V) bool) (ret map[K]V) {
	ret = make(map[K]V)
	for k, v := range m {
		if test(k, v) {
			ret[k] = v
		}
	}
	return
}
