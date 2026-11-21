package pki

import (
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

func WriteCertificateAndPrivateKeyToDisk(certificateBytes, keyBytes []byte, name, directory string) error {
	if directory != "" {
		certOut, err := os.Create(fmt.Sprintf("%s/%s.cert.pem", directory, name))
		if err != nil {
			return err
		}
		err = certOut.Chmod(0644)
		if err != nil {
			return err
		}
		keyOut, err := os.Create(fmt.Sprintf("%s/%s.key.pem", directory, name))
		if err != nil {
			return err
		}
		err = keyOut.Chmod(0600)
		if err != nil {
			return err
		}
		if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certificateBytes}); err != nil {
			return err
		}
		if err := certOut.Close(); err != nil {
			return err
		}
		if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes}); err != nil {
			return err
		}
		if err := keyOut.Close(); err != nil {
			return err
		}
		return nil
	}
	return errors.New("pki: no output directory specified for writing certificate and key")
}

// WritePEMToFile writes PEM-encoded DER bytes to a specific file path.
func WritePEMToFile(derBytes []byte, blockType, filePath string, mode os.FileMode) error {
	out, err := os.Create(filePath)
	if err != nil {
		return err
	}
	if err := out.Chmod(mode); err != nil {
		return err
	}
	if err := pem.Encode(out, &pem.Block{Type: blockType, Bytes: derBytes}); err != nil {
		return err
	}
	return out.Close()
}

// WriteCertAndKeyToFiles writes a DER certificate and key to the specified file paths as PEM.
func WriteCertAndKeyToFiles(certDER, keyDER []byte, certFile, keyFile string) error {
	if err := WritePEMToFile(certDER, "CERTIFICATE", certFile, 0644); err != nil {
		return fmt.Errorf("failed to write certificate: %w", err)
	}
	if err := WritePEMToFile(keyDER, "PRIVATE KEY", keyFile, 0600); err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}
	return nil
}
