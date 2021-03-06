package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/go-github/v24/github"
	"golang.org/x/oauth2"
	"gopkg.in/square/go-jose.v2/jwt"
	"io/ioutil"
	"log"
	"net/http"
)

func NewGHEClient(baseURL, uploadURL string) *GHEClient {
	return &GHEClient{baseURL: baseURL, uploadURL: uploadURL}
}

type GHEClient struct {
	baseURL   string
	uploadURL string
	client    *github.Client
}

func (c *GHEClient) Login(ctx context.Context, token string) error {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	client, err := github.NewEnterpriseClient(c.baseURL, c.uploadURL, tc)
	if err != nil {
		return err
	}
	c.client = client

	return nil
}

func Start(baseUrl string, uploadUrl string, org string) error {
	log.Printf("[INFO] START: baseUrl: %s, uploadUrl: %s, org: %s", baseUrl, uploadUrl, org)
	http.HandleFunc("/webhook", func(rw http.ResponseWriter, req *http.Request) {
		log.Println("[DEBUG] received")

		user, teams, err := checkToken(baseUrl, uploadUrl, org, req)
		if err != nil {
			http.Error(rw, err.Error(), 401)
		}
		if err != nil {
			aresp := &AuthenticationResponse{
				ApiVersion: "authentication.k8s.io/v1beta1",
				Kind:       "TokenReview",
				Status: Status{
					Authenticated: false,
					Error:         err.Error(),
				},
			}
			respBytes, err := json.Marshal(aresp)
			if err != nil {
				http.Error(rw, fmt.Sprintf("Failed to marshal: %s", err.Error()), 401)
				return
			}
			log.Printf("[DEBUG] %+v", aresp)
			fmt.Fprint(rw, string(respBytes))
		} else {
			aresp := &AuthenticationResponse{
				ApiVersion: "authentication.k8s.io/v1beta1",
				Kind:       "TokenReview",
				Status: Status{
					Authenticated: true,
					User: User{
						Username: *user.Login,
						Groups:   teams[org],
					},
				},
			}
			respBytes, err := json.Marshal(aresp)
			if err != nil {
				http.Error(rw, fmt.Sprintf("Failed to marshal: %s", err.Error()), 401)
				return
			}
			log.Printf("[DEBUG] %+v", aresp)
			fmt.Fprint(rw, string(respBytes))
		}
	})

	err := http.ListenAndServe("0.0.0.0:8443", nil)
	if err != nil {
		log.Fatal(err)
	}
	return nil
}

func getUserInfo(github_base_url string, token string) (github.User, error) {
	var u github.User
	req, _ := http.NewRequest("GET", github_base_url+"/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	client := new(http.Client)
	resp, err := client.Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return u, errors.New("Faield to read response body")
		}
		return u, errors.New(string(b))
	}

	decoder := json.NewDecoder(resp.Body)
	err = decoder.Decode(&u)
	if err != nil {
		return u, err
	}

	return u, nil
}

func (c *GHEClient) getTeams(ctx context.Context) (map[string][]string, error) {
	listOpt := &github.ListOptions{
		PerPage: 100,
	}
	var teams []*github.Team
	resp := map[string][]string{}

	for {
		tmpTeams, resp, err := c.client.Teams.ListUserTeams(ctx, listOpt)
		if err != nil {
			return map[string][]string{}, err
		}

		teams = append(teams, tmpTeams...)
		if resp.NextPage == 0 {
			break
		}
		listOpt.Page = resp.NextPage
	}

	for _, team := range teams {
		if resp[*team.Organization.Login] == nil {
			resp[*team.Organization.Login] = []string{}
		}
		resp[*team.Organization.Login] = append(resp[*team.Organization.Login], *team.Name)
	}

	return resp, nil
}

func checkToken(baseUrl string, uploadUrl string, org string, req *http.Request) (github.User, map[string][]string, error) {
	var areq AuthenticationRequest
	var user github.User
	var teams map[string][]string

	decoder := json.NewDecoder(req.Body)
	err := decoder.Decode(&areq)
	if err != nil {
		return user, teams, err
	}

	if areq.Spec.Token == "" {
		return user, teams, errors.New("token is empty")
	}
	isjwt, _ := isJWT(areq.Spec.Token)
	if isjwt {
		return user, teams, errors.New("token is unsupported format (JWT).")
	}

	fmt.Printf("[DEBUG] areq: %+v\n", areq)
	user, err = getUserInfo(baseUrl, areq.Spec.Token)
	if err != nil {
		return user, teams, errors.New(fmt.Sprintf("Failed to get user info: %s", err.Error()))
	}

	if user.Login == nil {
		return user, teams, errors.New("Failed to get user info")
	}

	gheClient := NewGHEClient(baseUrl, uploadUrl)
	err = gheClient.Login(req.Context(), areq.Spec.Token)
	if err != nil {
		return user, teams, fmt.Errorf("Failed to login to GHE: %s", err.Error())
	}

	teams, err = gheClient.getTeams(req.Context())
	if err != nil {
		return user, teams, fmt.Errorf("Failed to get teams: %s", err.Error())
	}
	return user, teams, nil
}

func isJWT(token string) (bool, error) {
	_, err := jwt.ParseSigned(token)
	if err != nil {
		return false, err
	}
	return true, nil
}
