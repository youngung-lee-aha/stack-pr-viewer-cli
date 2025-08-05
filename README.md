# Stacked PR Viewer

GitHub의 스택형 풀 리퀘스트 의존성을 분석하고 시각화하는 CLI 도구입니다. PR 설명에서 "depends on #123", "based on #123" 등의 패턴을 파싱하여 의존성 그래프를 구축합니다.

## 설치 및 빌드

### Go Install로 설치 (권장)
- mac예시
```bash
brew install go && echo 'export PATH=$PATH:$(go env GOPATH)/bin' >> ~/.zshrc && source ~/.zshrc
```

```bash
# 직접 설치 (GOPATH/bin에 자동 설치)
go install github.com/youngung-lee-aha/stack-pr-viewer-cli@latest
```

### 소스에서 빌드

```bash
# 레포지토리 클론
git clone https://github.com/youngung-lee-aha/stack-pr-viewer-cli.git
cd stack-pr-viewer-cli

# 의존성 설치
go mod tidy

# 바이너리 빌드
go build -o stacked-pr-viewer

# PATH에 설치 (선택사항)
sudo mv stacked-pr-viewer /usr/local/bin/
```

### 직접 실행

```bash
# 빌드 없이 직접 실행
go run main.go [PR_URL]
```

## 사용법

### 기본 사용

```bash
# PR URL로 스택 분석
./stacked-pr-viewer https://github.com/owner/repo/pull/123

# 토큰을 직접 제공
./stacked-pr-viewer -t YOUR_GITHUB_TOKEN https://github.com/owner/repo/pull/123
```

### 인증 설정

이 도구는 GitHub 인증이 필요합니다:

1. **GitHub CLI 사용 (권장)**:

   ```bash
   gh auth login
   ```

2. **개인 액세스 토큰 사용**:
   ```bash
   ./stacked-pr-viewer -t YOUR_TOKEN https://github.com/owner/repo/pull/123
   ```

## 기능

- PR 설명에서 의존성 자동 추출
- 스택형 PR의 토폴로지 정렬
- 의존성 그래프 시각화
- GitHub API를 통한 PR 메타데이터 수집

## 요구사항

- Go 1.21+
- GitHub CLI (`gh`) 또는 GitHub 개인 액세스 토큰
- 인터넷 연결 (GitHub API 접근)
