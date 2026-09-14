package ads

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOfficialSDKConstants(t *testing.T) {
	data, err := os.ReadFile("testdata/spec/beckhoff-sdk-constants.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Definitions map[string]map[string]uint32 `json:"definitions"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	groups := map[string]map[string]uint32{
		"AdsSymbolFlags":        {"Persistent": SymFlagPersistent, "BitValue": SymFlagBitValue, "ReferenceTo": SymFlagReferenceTo, "TypeGuid": SymFlagTypeGUID, "TComInterfacePtr": SymFlagInterfacePointer, "ReadOnly": SymFlagReadOnly, "ItfMethodAccess": SymFlagItfMethodAccess, "MethodDeref": SymFlagMethodDeref, "ContextMask": SymFlagContextMask, "Attributes": SymFlagAttributes, "Static": SymFlagStatic, "InitOnReset": SymFlagInitOnReset, "ExtendedFlags": SymFlagExtendedFlags},
		"AdsReservedIndexGroup": {"SymbolValueByName": IndexGroupSymbolValueByName, "SymbolHandleByName": IndexGroupSymbolHandleByName, "SymbolReleaseHandle": IndexGroupSymbolReleaseHandle, "SymbolVersion": IndexGroupSymbolVersion, "SymbolUpload": IndexGroupSymbolUpload, "SymbolUploadInfo": IndexGroupSymbolUploadInfo, "SumCommandRead": IndexGroupSumUpRead},
		"AdsErrorCode":          {"DeviceException": ErrDeviceException, "DeviceLicenseDuplicated": ErrDeviceLicenseDuplicated, "DeviceSignatureInvalid": ErrDeviceInvalidSignature, "DeviceCertificateInvalid": ErrDeviceCertInvalid, "DeviceLicenseOemNotFound": ErrDeviceLicenseOemNotFound, "DeviceLicenseRestricted": ErrDeviceLicenseRestricted, "DeviceLicenseDemoDenied": ErrDeviceLicenseDemoDenied, "DeviceInvalidFunctionId": ErrDeviceInvalidFncId, "DeviceOutOfRange": ErrDeviceOutOfRange, "DeviceInvalidAlignment": ErrDeviceInvalidAlignment, "DeviceLicensePlatform": ErrDeviceLicensePlatform, "DeviceContextForwardPassiveLevel": ErrDeviceContextFwd, "DeviceContextForwardDispatchLevel": ErrDeviceDispatch, "DeviceContextForwardRealTime": ErrDeviceRealTime, "DeviceCertificateEntrust": ErrDeviceCertificateEntrust, "DeviceLicenseIdNotUnique": ErrDeviceLicenseIdNotUnique, "DeviceNoRealtimeConfig": ErrDeviceNoRealtimeConfig, "DeviceLicenseFutureIssue": ErrDeviceLicenseFutureIssue, "DeviceLicenseTimeToLong": ErrDeviceLicenseTimeTooLong, "SyncTimeOut": ErrSyncTimeout, "AmsSyncAmsError": ErrAmsSyncAmsError, "TcpConnectionRefused": ErrTcpConnectionRefused, "PortAlreadyInUse": ErrRouterPortAlreadyInUse, "PortNotRegistered": ErrRouterPortNotRegistered, "NoMoreQueues": ErrRouterNoMoreQueues, "InvalidPort": ErrRouterInvalidPort, "RouterNotActive": ErrRouterNotActive, "RouterFragmentBoxFull": ErrRouterFragmentBoxFull, "RouterFragmentTimeout": ErrRouterFragmentTimeout, "RouterPortToBeRemoved": ErrRouterPortToBeRemoved},
		"AdsDataTypeFlags":      {"DataType": dtDataType, "DataItem": dtDataItem, "ReferenceTo": dtReference, "BitValues": dtBitValues, "TypeGuid": dtGUID, "Attributes": dtAttributes, "EnumInfos": dtEnums, "Static": dtStatic, "ExtendedFlags": dtExtendedFlags},
	}
	for family, actual := range groups {
		for name, got := range actual {
			want, ok := fixture.Definitions[family][name]
			if !ok || got != want {
				t.Errorf("%s.%s: 0x%x want 0x%x (present=%v)", family, name, got, want, ok)
			}
		}
	}
	if adsErrorName(0x72c) != "Device exception" || adsErrorName(0x72d) != "License duplicated" {
		t.Fatal("exception name regression")
	}
}
