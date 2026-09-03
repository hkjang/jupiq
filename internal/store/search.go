package store

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type SearchAccess struct {
	Users    bool
	Hubs     bool
	Servers  bool
	Projects bool
}

type SearchItem struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Path     string `json:"path"`
}

func searchPattern(query string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(query))
	return "%" + escaped + "%"
}

func (s *Store) GlobalSearch(ctx context.Context, query string, access SearchAccess, perType int) ([]SearchItem, error) {
	if perType < 1 || perType > 20 {
		perType = 8
	}
	pattern := searchPattern(query)
	items := []SearchItem{}
	if access.Users {
		rows, err := s.Pool.Query(ctx, `SELECT username,COALESCE(NULLIF(display_name,''),username),department FROM managed_users WHERE username ILIKE $1 ESCAPE '\' OR display_name ILIKE $1 ESCAPE '\' OR department ILIKE $1 ESCAPE '\' ORDER BY active DESC,last_activity_at DESC NULLS LAST LIMIT $2`, pattern, perType)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var username, title, department string
			if err := rows.Scan(&username, &title, &department); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, SearchItem{Type: "user", ID: username, Title: title, Subtitle: username + optionalSearchText(" · ", department), Path: "/users/" + url.PathEscape(username)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if access.Hubs {
		rows, err := s.Pool.Query(ctx, `SELECT id,name,network,base_url FROM hubs WHERE name ILIKE $1 ESCAPE '\' OR network ILIKE $1 ESCAPE '\' OR base_url ILIKE $1 ESCAPE '\' ORDER BY enabled DESC,name LIMIT $2`, pattern, perType)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var name, network, baseURL string
			if err := rows.Scan(&id, &name, &network, &baseURL); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, SearchItem{Type: "hub", ID: fmt.Sprint(id), Title: name, Subtitle: network + optionalSearchText(" · ", baseURL), Path: searchFilterPath("/hubs", name)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if access.Servers {
		rows, err := s.Pool.Query(ctx, `SELECT s.id,s.username,h.name,s.pod_name,s.status FROM servers s JOIN hubs h ON h.id=s.hub_id WHERE s.username ILIKE $1 ESCAPE '\' OR s.pod_name ILIKE $1 ESCAPE '\' OR s.node_name ILIKE $1 ESCAPE '\' OR h.name ILIKE $1 ESCAPE '\' ORDER BY (s.status='running') DESC,s.synced_at DESC LIMIT $2`, pattern, perType)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var username, hub, pod, status string
			if err := rows.Scan(&id, &username, &hub, &pod, &status); err != nil {
				rows.Close()
				return nil, err
			}
			subtitle := hub + optionalSearchText(" · ", pod) + optionalSearchText(" · ", status)
			items = append(items, SearchItem{Type: "server", ID: fmt.Sprint(id), Title: username, Subtitle: subtitle, Path: searchFilterPath("/servers", username)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if access.Projects {
		rows, err := s.Pool.Query(ctx, `SELECT id,name,status FROM resources WHERE kind='project' AND name ILIKE $1 ESCAPE '\' ORDER BY updated_at DESC LIMIT $2`, pattern, perType)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var name, status string
			if err := rows.Scan(&id, &name, &status); err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, SearchItem{Type: "project", ID: fmt.Sprint(id), Title: name, Subtitle: status, Path: searchFilterPath("/projects", name)})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return items, nil
}

func searchFilterPath(path, query string) string {
	values := url.Values{"search": []string{query}}
	return path + "?" + values.Encode()
}

func optionalSearchText(prefix, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return prefix + value
}
