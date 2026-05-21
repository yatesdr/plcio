module github.com/yatesdr/plcio

go 1.24.0

// v0.2.7 was a mis-tag placed on the module-rename commit before the 0.1.x
// release series. It predates PCCC support, multi-hop routing, SLC discovery,
// and the eipadapter package. The tag was removed from origin but Go module
// proxies cache versions immutably, so this retract directive tells the go
// toolchain not to select it via @latest.
retract v0.2.7
