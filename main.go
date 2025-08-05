package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type PullRequest struct {
	Number       int    `json:"number"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	State        string `json:"state"`
	BaseBranch   string `json:"-"`
	HeadBranch   string `json:"-"`
	Dependencies []int  `json:"-"`
}

type StackVisualizer struct {
	token  string
	client *http.Client
	cache  map[int]*PullRequest
}

func NewStackVisualizer(token string) *StackVisualizer {
	return &StackVisualizer{
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
		cache:  make(map[int]*PullRequest),
	}
}

// GitHub API 호출
func (sv *StackVisualizer) fetchPR(owner, repo string, number int) (*PullRequest, error) {
	if pr, exists := sv.cache[number]; exists {
		return pr, nil
	}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls/%d", owner, repo, number)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "token "+sv.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := sv.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(body))
	}

	var prData map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&prData); err != nil {
		return nil, err
	}

	// base와 head 브랜치 정보 추출
	baseBranch := ""
	headBranch := ""
	if base, ok := prData["base"].(map[string]interface{}); ok {
		if ref, ok := base["ref"].(string); ok {
			baseBranch = ref
		}
	}
	if head, ok := prData["head"].(map[string]interface{}); ok {
		if ref, ok := head["ref"].(string); ok {
			headBranch = ref
		}
	}

	pr := &PullRequest{
		Number:     int(prData["number"].(float64)),
		Title:      prData["title"].(string),
		Body:       prData["body"].(string),
		State:      prData["state"].(string),
		BaseBranch: baseBranch,
		HeadBranch: headBranch,
	}

	// Dependency 추출
	pr.Dependencies = extractDependencies(pr.Body)
	sv.cache[number] = pr

	return pr, nil
}

// PR 본문에서 dependency 추출
func extractDependencies(body string) []int {
	patterns := []string{
		`(?i)depends\s+on\s+#(\d+)`,
		`(?i)based\s+on\s+#(\d+)`,
		`(?i)stacked\s+on\s+#(\d+)`,
		`(?i)builds?\s+on\s+#(\d+)`,
		`(?i)requires?\s+#(\d+)`,
		`(?i)follows?\s+#(\d+)`,
	}

	var deps []int
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(body, -1)
		for _, match := range matches {
			if len(match) > 1 {
				if num, err := strconv.Atoi(match[1]); err == nil {
					deps = append(deps, num)
				}
			}
		}
	}

	// "stack:" 형식에서 의존성 추출
	deps = append(deps, extractStackDependencies(body)...)

	return deps
}

// "stack:" 형식에서 전체 스택 정보 추출
func extractStackInfo(body string) ([]int, int) {
	// stack: 섹션 찾기
	stackPattern := `(?i)stack:\s*\n((?:\s*-\s*#\d+[^\n]*\n?)*)`
	re := regexp.MustCompile(stackPattern)
	matches := re.FindStringSubmatch(body)
	
	if len(matches) < 2 {
		return nil, -1
	}
	
	stackSection := matches[1]
	
	// PR 번호들과 현재 위치 추출
	lines := strings.Split(strings.TrimSpace(stackSection), "\n")
	var stackPRs []int
	currentIndex := -1
	
	for i, line := range lines {
		// PR 번호 추출
		prPattern := `#(\d+)`
		prRe := regexp.MustCompile(prPattern)
		if match := prRe.FindStringSubmatch(line); len(match) > 1 {
			if num, err := strconv.Atoi(match[1]); err == nil {
				stackPRs = append(stackPRs, num)
				
				// 현재 PR 위치 확인 (<- 마커)
				if strings.Contains(line, "<-") {
					currentIndex = i
				}
			}
		}
	}
	
	return stackPRs, currentIndex
}

// "stack:" 형식에서 의존성 추출
func extractStackDependencies(body string) []int {
	return nil // 이제 사용하지 않음
}

// 모든 열린 PR 목록 가져오기
func (sv *StackVisualizer) fetchAllOpenPRs(owner, repo string) ([]int, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/pulls?state=open&per_page=100", owner, repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "token "+sv.token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := sv.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error %d: %s", resp.StatusCode, string(body))
	}

	var prs []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&prs); err != nil {
		return nil, err
	}

	var prNumbers []int
	for _, pr := range prs {
		prNumbers = append(prNumbers, int(pr["number"].(float64)))
	}

	return prNumbers, nil
}

// PR이 다른 PR에 의존하는지 확인
func (sv *StackVisualizer) findDependents(owner, repo string, targetPR int, allPRs []int) ([]int, error) {
	var dependents []int
	
	for _, prNum := range allPRs {
		if prNum == targetPR {
			continue
		}
		
		pr, err := sv.fetchPR(owner, repo, prNum)
		if err != nil {
			continue // 에러가 있는 PR은 건너뛰기
		}
		
		// 이 PR이 targetPR에 의존하는지 확인
		for _, dep := range pr.Dependencies {
			if dep == targetPR {
				dependents = append(dependents, prNum)
				break
			}
		}
	}
	
	return dependents, nil
}

// 브랜치 관계를 기반으로 관련 PR들 찾기
func (sv *StackVisualizer) findRelatedPRsByBranch(owner, repo string, targetPR *PullRequest, allPRs []int) ([]int, error) {
	var relatedPRs []int
	
	for _, prNum := range allPRs {
		if prNum == targetPR.Number {
			continue
		}
		
		pr, err := sv.fetchPR(owner, repo, prNum)
		if err != nil {
			continue
		}
		
		// 브랜치 관계 확인:
		// 1. 이 PR의 base가 targetPR의 head와 같으면 targetPR에 의존
		// 2. targetPR의 base가 이 PR의 head와 같으면 이 PR이 targetPR에 의존
		if pr.BaseBranch == targetPR.HeadBranch || targetPR.BaseBranch == pr.HeadBranch {
			relatedPRs = append(relatedPRs, prNum)
		}
	}
	
	return relatedPRs, nil
}

// Stacked PR 전체 그래프 구축
func (sv *StackVisualizer) buildStackGraph(owner, repo string, startPR int) ([]*PullRequest, error) {
	// 시작 PR 가져오기
	startPRData, err := sv.fetchPR(owner, repo, startPR)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch starting PR #%d: %w", startPR, err)
	}

	// 먼저 stack 형식이 있는지 확인
	stackPRs, _ := extractStackInfo(startPRData.Body)
	if stackPRs != nil && len(stackPRs) > 1 {
		// stack 형식을 찾았으면 해당 PR들을 모두 가져오기
		var stack []*PullRequest
		for _, prNum := range stackPRs {
			pr, err := sv.fetchPR(owner, repo, prNum)
			if err != nil {
				// 에러가 있는 PR은 건너뛰고 계속
				fmt.Printf("Warning: failed to fetch PR #%d: %v\n", prNum, err)
				continue
			}
			stack = append(stack, pr)
		}
		return stack, nil
	}

	// stack 형식이 없으면 다른 PR들의 stack 정보에서 이 PR이 포함된 것을 찾아보기
	allPRs, err := sv.fetchAllOpenPRs(owner, repo)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch all PRs: %w", err)
	}

	// 다른 PR들의 stack 정보를 확인하여 현재 PR이 포함된 stack 찾기
	for _, prNum := range allPRs {
		if prNum == startPR {
			continue
		}
		
		pr, err := sv.fetchPR(owner, repo, prNum)
		if err != nil {
			continue // 에러가 있는 PR은 건너뛰기
		}
		
		otherStackPRs, _ := extractStackInfo(pr.Body)
		if otherStackPRs != nil {
			// 현재 startPR이 이 stack에 포함되어 있는지 확인
			for _, stackPRNum := range otherStackPRs {
				if stackPRNum == startPR {
					// 찾았다! 이 stack의 모든 PR들을 가져오기
					var stack []*PullRequest
					for _, stackPR := range otherStackPRs {
						stackPRData, err := sv.fetchPR(owner, repo, stackPR)
						if err != nil {
							fmt.Printf("Warning: failed to fetch PR #%d: %v\n", stackPR, err)
							continue
						}
						stack = append(stack, stackPRData)
					}
					return stack, nil
				}
			}
		}
	}

	// stack 정보를 찾지 못했으면 브랜치 관계 기반으로 관련 PR들 찾기
	relatedPRs, err := sv.findRelatedPRsByBranch(owner, repo, startPRData, allPRs)
	if err == nil && len(relatedPRs) > 0 {
		// 브랜치 관계를 통해 연결된 모든 PR들을 재귀적으로 탐색
		visited := make(map[int]bool)
		var stack []*PullRequest
		
		var traverseBranches func(int) error
		traverseBranches = func(prNum int) error {
			if visited[prNum] {
				return nil
			}
			visited[prNum] = true
			
			pr, err := sv.fetchPR(owner, repo, prNum)
			if err != nil {
				return err
			}
			
			stack = append(stack, pr)
			
			// 이 PR과 브랜치 관계가 있는 다른 PR들 찾기
			branchRelated, err := sv.findRelatedPRsByBranch(owner, repo, pr, allPRs)
			if err != nil {
				return err
			}
			
			for _, relatedNum := range branchRelated {
				if err := traverseBranches(relatedNum); err != nil {
					return err
				}
			}
			
			return nil
		}
		
		if err := traverseBranches(startPR); err == nil && len(stack) > 1 {
			// 브랜치 dependency 순서로 정렬
			sort.Slice(stack, func(i, j int) bool {
				// main에 가까운 것부터 (base가 main인 것들을 먼저)
				if stack[i].BaseBranch == "main" && stack[j].BaseBranch != "main" {
					return true
				}
				if stack[i].BaseBranch != "main" && stack[j].BaseBranch == "main" {
					return false
				}
				return stack[i].Number < stack[j].Number
			})
			return stack, nil
		}
	}

	// 마지막으로 전통적인 dependency 기반 탐색
	visited := make(map[int]bool)
	var stack []*PullRequest

	var traverse func(int) error
	traverse = func(prNum int) error {
		if visited[prNum] {
			return nil
		}
		visited[prNum] = true

		pr, err := sv.fetchPR(owner, repo, prNum)
		if err != nil {
			return fmt.Errorf("failed to fetch PR #%d: %w", prNum, err)
		}

		stack = append(stack, pr)

		// 의존성 재귀 탐색 (아래쪽 스택)
		for _, dep := range pr.Dependencies {
			if err := traverse(dep); err != nil {
				return err
			}
		}

		// 의존하는 PR들 찾기 (위쪽 스택)
		dependents, err := sv.findDependents(owner, repo, prNum, allPRs)
		if err != nil {
			return fmt.Errorf("failed to find dependents for PR #%d: %w", prNum, err)
		}

		for _, dependent := range dependents {
			if err := traverse(dependent); err != nil {
				return err
			}
		}

		return nil
	}

	if err := traverse(startPR); err != nil {
		return nil, err
	}

	// 스택을 의존성 순서대로 정렬 (의존성이 적은 것부터 = 베이스에 가까운 것부터)
	sort.Slice(stack, func(i, j int) bool {
		return len(stack[i].Dependencies) < len(stack[j].Dependencies)
	})

	return stack, nil
}

// 간단한 텍스트 스택 출력
func printStack(stack []*PullRequest, currentPR int) {
	// 첫 번째 형식: 상세 정보
	fmt.Println("\nstack:")
	
	for _, pr := range stack {
		marker := ""
		if pr.Number == currentPR {
			marker = " <-"
		}
		
		status := "open"
		if pr.State == "closed" {
			status = "closed"
		} else if pr.State == "draft" {
			status = "draft"
		}
		
		fmt.Printf("- #%d (%s): %s%s\n", pr.Number, status, pr.Title, marker)
	}
	
	// 구분선
	fmt.Println("--------")
	
	// 두 번째 형식: git pr 용 간단한 형식 (숫자만)
	for _, pr := range stack {
		marker := ""
		if pr.Number == currentPR {
			marker = " <-"
		}
		fmt.Printf("- #%d%s\n", pr.Number, marker)
	}
}

// GitHub 토큰 가져오기
func getGitHubToken(flagToken string) (string, error) {
	if flagToken != "" {
		return flagToken, nil
	}

	cmd := exec.Command("gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh CLI not authenticated. Run: gh auth login")
	}
	
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", fmt.Errorf("empty token from gh CLI")
	}
	
	return token, nil
}

// URL 파싱
func parseGitHubURL(url string) (owner, repo string, prNumber int, err error) {
	re := regexp.MustCompile(`github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	matches := re.FindStringSubmatch(url)
	
	if len(matches) != 4 {
		return "", "", 0, fmt.Errorf("invalid GitHub PR URL format")
	}

	owner = matches[1]
	repo = matches[2]
	prNumber, err = strconv.Atoi(matches[3])
	
	return
}

func main() {
	var token string

	rootCmd := &cobra.Command{
		Use:   "stacked-pr [PR_URL]",
		Short: "Show stacked GitHub PRs in simple text format",
		Long: `A minimal CLI tool to analyze GitHub PR dependencies.

Requires gh CLI authentication: gh auth login

Examples:
  stacked-pr https://github.com/owner/repo/pull/123`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prURL := args[0]

			// 토큰 가져오기
			resolvedToken, err := getGitHubToken(token)
			if err != nil {
				return err
			}

			// URL 파싱
			owner, repo, prNumber, err := parseGitHubURL(prURL)
			if err != nil {
				return err
			}

			fmt.Printf("Analyzing %s/%s #%d...\n", owner, repo, prNumber)

			// Stack 분석
			visualizer := NewStackVisualizer(resolvedToken)
			stack, err := visualizer.buildStackGraph(owner, repo, prNumber)
			if err != nil {
				return err
			}

			// 스택 출력
			printStack(stack, prNumber)

			return nil
		},
	}

	rootCmd.Flags().StringVarP(&token, "token", "t", "", "GitHub personal access token")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
