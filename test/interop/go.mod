module github.com/tumour/awg-mesh/test/interop

go 1.27.4

require (
	github.com/amnezia-vpn/amneziawg-go v0.2.19
	github.com/amnezia-vpn/amneziawg-go/v3 v3.1.20261116
	github.com/tumour/awg-mesh v0.0.0
)

require (
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

replace github.com/tumour/awg-mesh => ../..
