package cip

type Request struct {
	Service byte
	Path    EPath_t
	Data    []byte
}

func (r Request) Marshal() []byte {
	path := r.Path
	out := make([]byte, 0, 2+len(path)+len(r.Data))
	out = append(out, r.Service)
	out = append(out, r.Path.WordLen())
	out = append(out, path...)
	out = append(out, r.Data...)
	return out
}

type Response struct {
	ReplyService     byte
	GeneralStatus    byte
	AdditionalStatus []uint16
	Data             []byte
}
