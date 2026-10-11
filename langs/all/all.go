// Package all links every language module into the binary.
//
// Each module is imported from its own file guarded by a build tag, so a language can be left
// out with -tags no_<name>, for example:
//
//	//go:build !no_yaml
//	package all
//	import _ "github.com/xsak/bin-language-servers/langs/yaml"
package all
