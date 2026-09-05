package main

import (
	"archive/zip"
	"encoding/binary"
	"howett.net/plist"
	"os"
	"testing"
)

func testProtoBytes(number int, body []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(number<<3|2))
	out = binary.AppendUvarint(out, uint64(len(body)))
	return append(out, body...)
}
func testAndroidManifest(id, version, build string) []byte {
	element := testProtoBytes(3, []byte("manifest"))
	for _, pair := range [][2]string{{"package", id}, {"versionName", version}, {"versionCode", build}} {
		attribute := testProtoBytes(2, []byte(pair[0]))
		attribute = append(attribute, testProtoBytes(3, []byte(pair[1]))...)
		if pair[0] != "package" {
			attribute = append(attribute, testProtoBytes(1, []byte("http://schemas.android.com/apk/res/android"))...)
		}
		element = append(element, testProtoBytes(4, attribute)...)
	}
	return testProtoBytes(1, element)
}
func writeMobileFixture(t *testing.T, path, platform, id, version, build string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	name := "base/manifest/AndroidManifest.xml"
	body := testAndroidManifest(id, version, build)
	if platform == "ios" {
		name = "Payload/Example.app/Info.plist"
		body, err = plist.Marshal(map[string]string{"CFBundleIdentifier": id, "CFBundleShortVersionString": version, "CFBundleVersion": build}, plist.XMLFormat)
		if err != nil {
			t.Fatal(err)
		}
	}
	entry, err := writer.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
}
