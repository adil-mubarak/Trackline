package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxTaskSummaryProjects = 50

type taskSummaryProject struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

type projectDoneAnalytics struct {
	ProjectID uint64 `json:"project_id"`
	ByStatus  struct {
		Done uint64 `json:"done"`
	} `json:"by_status"`
	DoneByAssignee []struct {
		AssigneeID uint64 `json:"assignee_id"`
		Count      uint64 `json:"count"`
	} `json:"done_by_assignee"`
	DoneUnassigned uint64 `json:"done_unassigned"`
}

type userDoneTotal struct {
	ID            uint64
	Username      string
	Count         uint64
	ProjectCounts map[uint64]uint64
}

type doneProjectResult struct {
	Project taskSummaryProject
	Counts  projectDoneAnalytics
	Err     error
}

func requestsDoneTaskSummary(message string) bool {
	text := strings.ToLower(message)
	hasDone := strings.Contains(text, "done") || strings.Contains(text, "completed")
	hasTask := strings.Contains(text, "task") || strings.Contains(text, "issue")
	hasPerson := strings.Contains(text, "user") || strings.Contains(text, "employee") || strings.Contains(text, "assignee") || strings.Contains(text, "person")
	hasCrossProject := strings.Contains(text, "every project") || strings.Contains(text, "all projects") || strings.Contains(text, "across projects") || strings.Contains(text, "overall")
	return hasDone && hasTask && hasPerson && hasCrossProject
}

func requestsIssueCountChart(message string) bool {
	text := strings.ToLower(message)
	wantsChart := strings.Contains(text, "graph") || strings.Contains(text, "chart") || strings.Contains(text, "plot") || strings.Contains(text, "visual")
	metric := strings.Contains(text, "done") || strings.Contains(text, "completed") || strings.Contains(text, "performance") || strings.Contains(text, "workload")
	peopleOrProjects := strings.Contains(text, "user") || strings.Contains(text, "employee") || strings.Contains(text, "assignee") || strings.Contains(text, "team") || strings.Contains(text, "project")
	return wantsChart && metric && peopleOrProjects
}

func summarizeDoneTasksAcrossProjects(ctx context.Context, gatewayURL, bearerToken, requestID string, client *http.Client) (string, error) {
	result, err := summarizeDoneTasksAcrossProjectsWithCharts(ctx, gatewayURL, bearerToken, requestID, client, "all projects")
	return result.Reply, err
}

func summarizeDoneTasksAcrossProjectsWithCharts(ctx context.Context, gatewayURL, bearerToken, requestID string, client *http.Client, question string) (chatResult, error) {
	if strings.TrimSpace(bearerToken) == "" || strings.TrimSpace(gatewayURL) == "" {
		return chatResult{}, errors.New("authenticated tracker access is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 7 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	projectsData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, "/projects", nil)
	if err != nil {
		return chatResult{}, err
	}
	var projects []taskSummaryProject
	if json.Unmarshal(projectsData, &projects) != nil {
		return chatResult{}, errors.New("invalid accessible projects response")
	}
	if len(projects) > maxTaskSummaryProjects {
		return chatResult{}, errors.New("accessible project count exceeds the complete-summary limit")
	}
	if len(projects) == 0 {
		return chatResult{Reply: "No accessible projects were found for your account.", Model: "tracker-data-summary"}, nil
	}
	seenProjects := make(map[uint64]struct{}, len(projects))
	for _, project := range projects {
		if project.ID == 0 {
			return chatResult{}, errors.New("invalid project ID in accessible projects response")
		}
		if _, exists := seenProjects[project.ID]; exists {
			return chatResult{}, errors.New("duplicate project ID in accessible projects response")
		}
		seenProjects[project.ID] = struct{}{}
	}

	jobs := make(chan taskSummaryProject)
	results := make(chan doneProjectResult, len(projects))
	workerCount := min(8, len(projects))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for project := range jobs {
				query := url.Values{}
				query.Set("project_id", strconv.FormatUint(project.ID, 10))
				query.Set("include_assignees", "true")
				data, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodGet, "/issues/analytics?"+query.Encode(), nil)
				result := doneProjectResult{Project: project, Err: err}
				if err == nil {
					err = json.Unmarshal(data, &result.Counts)
					if err == nil && result.Counts.ProjectID != project.ID {
						err = errors.New("analytics response project ID mismatch")
					}
					result.Err = err
				}
				results <- result
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, project := range projects {
			select {
			case jobs <- project:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	byUser := make(map[uint64]*userDoneTotal)
	projectByID := make(map[uint64]string, len(projects))
	for _, project := range projects {
		projectByID[project.ID] = project.Name
	}
	var unassignedDone, totalDone uint64
	completedProjects := 0
	projectCounts := make([]aiChartPoint, 0, len(projects))
	for result := range results {
		if result.Err != nil {
			cancel()
			return chatResult{}, fmt.Errorf("could not retrieve complete issue data for project %d: %w", result.Project.ID, result.Err)
		}
		completedProjects++
		projectCounts = append(projectCounts, aiChartPoint{Label: result.Project.Name, Value: result.Counts.ByStatus.Done})
		unassignedDone += result.Counts.DoneUnassigned
		for _, item := range result.Counts.DoneByAssignee {
			if item.AssigneeID == 0 {
				return chatResult{}, errors.New("analytics returned an invalid assignee ID")
			}
			total := byUser[item.AssigneeID]
			if total == nil {
				total = &userDoneTotal{ID: item.AssigneeID, ProjectCounts: make(map[uint64]uint64)}
				byUser[item.AssigneeID] = total
			}
			total.Count += item.Count
			total.ProjectCounts[result.Project.ID] += item.Count
			totalDone += item.Count
		}
	}
	if completedProjects != len(projects) {
		return chatResult{}, errors.New("could not retrieve complete issue totals")
	}
	if len(byUser) > 500 {
		return chatResult{}, errors.New("assignee count exceeds complete-summary limit")
	}

	userIDs := make([]uint64, 0, len(byUser))
	for id := range byUser {
		userIDs = append(userIDs, id)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	for start := 0; start < len(userIDs); start += 100 {
		end := min(start+100, len(userIDs))
		body, err := json.Marshal(struct {
			UserIDs []uint64 `json:"user_ids"`
		}{UserIDs: userIDs[start:end]})
		if err != nil {
			return chatResult{}, fmt.Errorf("encode user-name lookup: %w", err)
		}
		namesData, err := callGatewayJSON(ctx, client, gatewayURL, bearerToken, requestID, http.MethodPost, "/users/names", body)
		if err != nil {
			return chatResult{}, fmt.Errorf("resolve assignee names: %w", err)
		}
		var names []struct {
			ID       uint64 `json:"id"`
			Username string `json:"username"`
		}
		if json.Unmarshal(namesData, &names) != nil {
			return chatResult{}, errors.New("invalid user-name lookup response")
		}
		for _, name := range names {
			if total := byUser[name.ID]; total != nil && strings.TrimSpace(name.Username) != "" {
				total.Username = strings.TrimSpace(name.Username)
			}
		}
	}

	users := make([]*userDoneTotal, 0, len(byUser))
	for _, total := range byUser {
		if total.Username == "" {
			total.Username = "Unknown user"
		}
		users = append(users, total)
	}
	sort.Slice(users, func(i, j int) bool {
		left, right := strings.ToLower(users[i].Username), strings.ToLower(users[j].Username)
		if left == right {
			return users[i].ID < users[j].ID
		}
		return left < right
	})

	var output strings.Builder
	fmt.Fprintf(&output, "**Issues marked done across your %d accessible projects** (all current records; no date filter)\n\n", len(projects))
	output.WriteString("These are tracker status counts, not verified work quality or employee performance. The list is not ranked.\n\n")
	if len(users) == 0 {
		output.WriteString("No assigned issues are currently marked done.\n")
	} else {
		output.WriteString("| User | Issues marked done | Contributing projects |\n|---|---:|---|\n")
		for _, user := range users {
			projectIDs := make([]uint64, 0, len(user.ProjectCounts))
			for projectID := range user.ProjectCounts {
				projectIDs = append(projectIDs, projectID)
			}
			sort.Slice(projectIDs, func(i, j int) bool { return projectIDs[i] < projectIDs[j] })
			contributions := make([]string, 0, len(projectIDs))
			for _, projectID := range projectIDs {
				name := strings.ReplaceAll(strings.ReplaceAll(projectByID[projectID], "|", "\\|"), "\n", " ")
				contributions = append(contributions, fmt.Sprintf("%s (%d)", name, user.ProjectCounts[projectID]))
			}
			username := strings.ReplaceAll(strings.ReplaceAll(user.Username, "|", "\\|"), "\n", " ")
			fmt.Fprintf(&output, "| %s | %d | %s |\n", username, user.Count, strings.Join(contributions, ", "))
		}
	}
	fmt.Fprintf(&output, "\nUnassigned issues marked done: %d. Total assigned issues marked done: %d. Assigned counts are aggregated across projects you can access.", unassignedDone, totalDone)
	if output.Len() > 32<<10 {
		return chatResult{}, errors.New("formatted task summary exceeds response limit")
	}
	result := chatResult{Reply: output.String(), Model: "tracker-data-summary"}
	text := strings.ToLower(question)
	if strings.Contains(text, "project") {
		sort.Slice(projectCounts, func(i, j int) bool {
			return strings.ToLower(projectCounts[i].Label) < strings.ToLower(projectCounts[j].Label)
		})
		result.Charts = append(result.Charts, aiChart{Type: "bar", Title: "Current issues marked done by project", Note: "One current status snapshot; this is not a delivery-performance score or a trend over time.", Data: projectCounts})
	}
	if strings.Contains(text, "user") || strings.Contains(text, "employee") || strings.Contains(text, "assignee") || strings.Contains(text, "team") {
		userPoints := make([]aiChartPoint, 0, len(users))
		for _, user := range users {
			userPoints = append(userPoints, aiChartPoint{Label: user.Username, Value: user.Count})
		}
		result.Charts = append(result.Charts, aiChart{Type: "bar", Title: "Issues marked done by assignee", Note: "Listed alphabetically, not ranked. Counts reflect tracker status only and are not employee performance scores.", Data: userPoints})
	}
	return result, nil
}
