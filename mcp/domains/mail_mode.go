package main

import "errors"

func setNamecheapMailMode(prov dnsProviderImpl, args map[string]any) error {
	mode := strArg(args, "namecheap_email_type")
	if mode == "" {
		return nil
	}
	p, ok := prov.(*namecheapProvider)
	if !ok {
		return errors.New("namecheap_email_type is only supported for Namecheap")
	}
	if !includes([]string{"MX", "MXE", "FWD", "OX"}, mode) {
		return errors.New("invalid Namecheap mail routing mode")
	}
	p.emailTypeOverride = mode
	return nil
}
