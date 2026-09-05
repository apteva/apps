package main

import (
	"archive/zip"
	"encoding/binary"
	"errors"
	"fmt"
	"howett.net/plist"
	"io"
	"strconv"
	"strings"
)

type mobileBinaryIdentity struct{ Identifier, Version, Build string }

func zipMemberBytes(file *zip.File) ([]byte, error) {
	const limit = 16 << 20
	if file.UncompressedSize64 > limit {
		return nil, errors.New("mobile metadata too large")
	}
	r, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if len(b) > limit {
		return nil, errors.New("mobile metadata too large")
	}
	return b, err
}
func readMobileBinaryIdentity(path, platform string) (mobileBinaryIdentity, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return mobileBinaryIdentity{}, err
	}
	defer zr.Close()
	var member *zip.File
	for _, f := range zr.File {
		match := platform == "android" && f.Name == "base/manifest/AndroidManifest.xml"
		if platform == "ios" {
			parts := strings.Split(f.Name, "/")
			match = len(parts) == 3 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") && parts[2] == "Info.plist"
		}
		if match {
			if member != nil {
				return mobileBinaryIdentity{}, errors.New("ambiguous mobile artifact")
			}
			member = f
		}
	}
	if member == nil {
		return mobileBinaryIdentity{}, errors.New("mobile binary has no application manifest")
	}
	body, err := zipMemberBytes(member)
	if err != nil {
		return mobileBinaryIdentity{}, err
	}
	var result mobileBinaryIdentity
	if platform == "ios" {
		var info struct {
			ID      string `plist:"CFBundleIdentifier"`
			Version string `plist:"CFBundleShortVersionString"`
			Build   string `plist:"CFBundleVersion"`
		}
		if _, err = plist.Unmarshal(body, &info); err != nil {
			return result, err
		}
		result = mobileBinaryIdentity{info.ID, info.Version, info.Build}
	} else {
		node, err := protobufFields(body)
		if err != nil {
			return result, err
		}
		element, err := protobufFields(protoBytes(node, 1))
		if err != nil {
			return result, err
		}
		if string(protoBytes(element, 3)) != "manifest" {
			return result, errors.New("AAB root is not manifest")
		}
		attrs := map[string]string{}
		for _, field := range element {
			if field.number != 4 {
				continue
			}
			a, err := protobufFields(field.bytes)
			if err != nil {
				return result, err
			}
			name := string(protoBytes(a, 2))
			namespace := string(protoBytes(a, 1))
			if name != "package" && namespace != "http://schemas.android.com/apk/res/android" {
				continue
			}
			value := string(protoBytes(a, 3))
			if value == "" {
				item, _ := protobufFields(protoBytes(a, 6))
				str, _ := protobufFields(protoBytes(item, 2))
				value = string(protoBytes(str, 1))
				if value == "" {
					primitive, _ := protobufFields(protoBytes(item, 7))
					for _, v := range primitive {
						if v.number == 6 || v.number == 7 {
							value = strconv.FormatUint(v.integer, 10)
						}
					}
				}
			}
			attrs[name] = value
		}
		result = mobileBinaryIdentity{attrs["package"], attrs["versionName"], attrs["versionCode"]}
	}
	if result.Identifier == "" || result.Version == "" || result.Build == "" {
		return result, errors.New("mobile manifest is missing identifier or version")
	}
	return result, nil
}
func verifyMobileBinaryIdentity(path, platform string, cfg mobileTargetConfig) (mobileBinaryIdentity, error) {
	actual, err := readMobileBinaryIdentity(path, platform)
	if err != nil {
		return actual, err
	}
	id, build := cfg.BundleID, cfg.BuildNumber
	if platform == "android" {
		id, build = cfg.PackageName, cfg.VersionCode
	}
	for _, pair := range [][3]string{{"identifier", id, actual.Identifier}, {"version", cfg.VersionName, actual.Version}, {"build", build, actual.Build}} {
		if pair[1] != "" && pair[1] != pair[2] {
			return actual, fmt.Errorf("mobile artifact %s mismatch: expected %q, found %q", pair[0], pair[1], pair[2])
		}
	}
	return actual, nil
}

// AAPT2 Resources.proto: XmlNode.element=1, XmlElement.attribute=4,
// XmlAttribute.{namespace_uri,name,value,compiled_item}=1,2,3,6.
// Unknown protobuf fields are skipped; malformed wire lengths fail closed.
type protobufField struct {
	number  int
	bytes   []byte
	integer uint64
}

func protoBytes(fields []protobufField, number int) []byte {
	for _, f := range fields {
		if f.number == number {
			return f.bytes
		}
	}
	return nil
}
func protobufFields(body []byte) ([]protobufField, error) {
	var out []protobufField
	for len(body) > 0 {
		tag, n := binary.Uvarint(body)
		if n <= 0 || tag>>3 == 0 {
			return nil, errors.New("invalid protobuf tag")
		}
		body = body[n:]
		f := protobufField{number: int(tag >> 3)}
		switch tag & 7 {
		case 0:
			v, n := binary.Uvarint(body)
			if n <= 0 {
				return nil, errors.New("invalid protobuf integer")
			}
			body = body[n:]
			f.integer = v
		case 1:
			if len(body) < 8 {
				return nil, io.ErrUnexpectedEOF
			}
			body = body[8:]
		case 2:
			size, n := binary.Uvarint(body)
			if n <= 0 || size > uint64(len(body)-n) {
				return nil, io.ErrUnexpectedEOF
			}
			body = body[n:]
			f.bytes = body[:int(size)]
			body = body[int(size):]
		case 5:
			if len(body) < 4 {
				return nil, io.ErrUnexpectedEOF
			}
			body = body[4:]
		default:
			return nil, errors.New("unsupported protobuf wire type")
		}
		out = append(out, f)
	}
	return out, nil
}
