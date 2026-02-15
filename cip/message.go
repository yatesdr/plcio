package cip

type MessageRouterRequest struct {
	Service         byte
	RequestPathSize byte
	RequestPath     EPath_t
	RequestData     []byte
}

type MessageRouterResponse struct {
	Service          byte
	GeneralStatus    byte
	AdditionalStatus []byte // Array of word / uint16
	ResponseData     []byte
}
