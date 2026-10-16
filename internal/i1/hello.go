package i1

import (
	"fmt"
	"maps"
	"slices"

	tls "github.com/refraction-networking/utls"
)

var fingerprints = map[string]tls.ClientHelloID{
	"chrome":  tls.HelloChrome_120,
	"firefox": tls.HelloFirefox_120,
	"safari":  tls.HelloSafari_16_0,
}

func Fingerprints() []string {
	return slices.Sorted(maps.Keys(fingerprints))
}

func clientHello(fingerprint, sni string, scid []byte) ([]byte, error) {
	id, ok := fingerprints[fingerprint]
	if !ok {
		return nil, fmt.Errorf("unknown fingerprint %q", fingerprint)
	}
	spec, err := tls.UTLSIdToSpec(id)
	if err != nil {
		return nil, err
	}
	quicSpec(&spec, scid)
	conn := tls.UClient(nil, &tls.Config{
		ServerName: sni,
		NextProtos: []string{"h3"},
		MinVersion: tls.VersionTLS13,
	}, tls.HelloCustom)
	if err := conn.ApplyPreset(&spec); err != nil {
		return nil, err
	}
	if err := conn.BuildHandshakeState(); err != nil {
		return nil, err
	}
	return conn.HandshakeState.Hello.Raw, nil
}

func quicSpec(spec *tls.ClientHelloSpec, scid []byte) {
	spec.TLSVersMin = tls.VersionTLS13
	spec.TLSVersMax = tls.VersionTLS13
	spec.CipherSuites = slices.DeleteFunc(spec.CipherSuites, func(c uint16) bool {
		return !isGrease(c) && c != tls.TLS_AES_128_GCM_SHA256 && c != tls.TLS_AES_256_GCM_SHA384 && c != tls.TLS_CHACHA20_POLY1305_SHA256
	})
	var exts []tls.TLSExtension
	for _, e := range spec.Extensions {
		switch x := e.(type) {
		case *tls.UtlsPaddingExtension, *tls.SessionTicketExtension, *tls.ExtendedMasterSecretExtension,
			*tls.RenegotiationInfoExtension, *tls.UtlsCompressCertExtension, *tls.ApplicationSettingsExtension,
			*tls.ApplicationSettingsExtensionNew, *tls.FakeChannelIDExtension, *tls.FakeRecordSizeLimitExtension:
			continue
		case *tls.ALPNExtension:
			x.AlpnProtocols = []string{"h3"}
		case *tls.SupportedVersionsExtension:
			x.Versions = slices.DeleteFunc(x.Versions, func(v uint16) bool { return !isGrease(v) && v != tls.VersionTLS13 })
		case *tls.SupportedPointsExtension:
			continue
		case *tls.KeyShareExtension:
			x.KeyShares = slices.DeleteFunc(x.KeyShares, func(k tls.KeyShare) bool {
				return !isGrease(uint16(k.Group)) && k.Group != tls.X25519
			})
		case *tls.SupportedCurvesExtension:
			x.Curves = slices.DeleteFunc(x.Curves, func(c tls.CurveID) bool {
				return !isGrease(uint16(c)) && c != tls.X25519 && c != tls.CurveP256 && c != tls.CurveP384 && c != tls.CurveP521
			})
		}
		exts = append(exts, e)
	}
	exts = append(exts, &tls.QUICTransportParametersExtension{TransportParameters: transportParameters(scid)})
	spec.Extensions = exts
}

func transportParameters(scid []byte) tls.TransportParameters {
	return tls.TransportParameters{
		tls.MaxIdleTimeout(30000),
		tls.MaxUDPPayloadSize(1472),
		tls.InitialMaxData(15728640),
		tls.InitialMaxStreamDataBidiLocal(6291456),
		tls.InitialMaxStreamDataBidiRemote(6291456),
		tls.InitialMaxStreamDataUni(6291456),
		tls.InitialMaxStreamsBidi(100),
		tls.InitialMaxStreamsUni(103),
		tls.MaxAckDelay(25),
		tls.InitialSourceConnectionID(scid),
		tls.MaxDatagramFrameSize(65536),
		&tls.GREASEQUICBit{},
	}
}

func isGrease(v uint16) bool {
	return v&0x0f0f == 0x0a0a && v>>8 == v&0xff
}
