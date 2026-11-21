package user

import (
	"fmt"

	"github.com/nats-io/nkeys"
)

type Service struct {
	UserBase
	Name string `json:"name"`
}

func NewService(serviceConfig *UserConfig, signingKeyPair nkeys.KeyPair) (*Service, error) {
	service := Service{
		UserBase: UserBase{
			AccountID:   serviceConfig.AccountID,
			ID:          fmt.Sprintf("%v:%v", serviceConfig.AccountID, serviceConfig.Name),
			Roles:       serviceConfig.Roles,
			Permissions: serviceConfig.Permissions,
		},
		Name: serviceConfig.Name,
	}
	err := service.New(signingKeyPair)
	if err != nil {
		return &service, err
	}
	return &service, nil
}

func (service *Service) Credentials() (*Credentials, error) {
	var credentials Credentials
	credentials.JWT = service.Token
	credentials.Seed = service.Seed
	credentials.AddTrustedAccount(service.AccountID)
	return &credentials, nil
}
