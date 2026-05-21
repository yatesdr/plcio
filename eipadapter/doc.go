// Package eipadapter implements the adapter (server) side of EtherNet/IP for
// Go applications that want to be scanned by a PLC. It is the inverse of the
// plcio scanner-side drivers: instead of dialing a PLC and reading tags, your
// process listens on TCP/UDP 44818, advertises an identity, exposes Assembly
// objects, and produces Class 1 cyclic I/O at the negotiated RPI.
//
// Typical use:
//
//	ident := eipadapter.Identity{
//	    VendorID:     0x1337,
//	    DeviceType:   0x000C,
//	    ProductCode:  1,
//	    RevMajor:     1, RevMinor: 0,
//	    ProductName:  "My Device",
//	    SerialNumber: 0xC0FFEE01,
//	}
//	input := eipadapter.NewAssembly(101, eipadapter.AssemblyInput, 16)
//	output := eipadapter.NewAssembly(102, eipadapter.AssemblyOutput, 4)
//	config := eipadapter.NewAssembly(103, eipadapter.AssemblyConfig, 0)
//
//	adp, err := eipadapter.New(eipadapter.Config{
//	    Identity:   ident,
//	    Assemblies: []*eipadapter.Assembly{input, output, config},
//	})
//	if err != nil { log.Fatal(err) }
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	go adp.Serve(ctx)
//
//	// Update input bytes from your application — the producer pushes them
//	// at the RPI.
//	input.SetBytes(0, []byte{statusByte, heartbeat, 0, 0})
//
// IMPORTANT: this package is NOT safety-rated. Do not use it to grant a
// permissive output for any safety function. See the plcio top-level
// Safety & Intended Use documentation.
package eipadapter
