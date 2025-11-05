use anyhow::{Context, Result};
use clap::Parser;
use regex::Regex;
use serde::Deserialize;
use std::collections::{HashMap, HashSet};
use std::process::Command;

#[derive(Parser)]
#[command(name = "stacked-pr")]
#[command(about = "Show stacked GitHub PRs in simple text format")]
struct Cli {
    /// GitHub PR URL
    pr_url: String,
}

#[derive(Deserialize, Clone)]
struct PullRequest {
    number: u32,
    title: String,
    body: String,
    state: String,
    #[serde(rename = "baseRefName")]
    base_ref: String,
    #[serde(rename = "headRefName")]
    head_ref: String,
}

struct StackVisualizer {
    cache: HashMap<u32, PullRequest>,
}

impl StackVisualizer {
    fn new() -> Self {
        Self { cache: HashMap::new() }
    }

    fn fetch_pr(&mut self, owner: &str, repo: &str, number: u32) -> Result<PullRequest> {
        if let Some(pr) = self.cache.get(&number) {
            return Ok(pr.clone());
        }

        let output = Command::new("gh")
            .env("GITHUB_TOKEN", "")
            .args(["pr", "view", &number.to_string(), "--repo", &format!("{}/{}", owner, repo), "--json", "number,title,body,state,baseRefName,headRefName"])
            .output()?;

        if !output.status.success() {
            anyhow::bail!("Failed to fetch PR #{}: {}", number, String::from_utf8_lossy(&output.stderr));
        }

        let pr: PullRequest = serde_json::from_slice(&output.stdout)?;
        self.cache.insert(number, pr.clone());
        Ok(pr)
    }

    fn fetch_all_open_prs(&self, owner: &str, repo: &str) -> Result<Vec<u32>> {
        let output = Command::new("gh")
            .env("GITHUB_TOKEN", "")
            .args(["pr", "list", "--repo", &format!("{}/{}", owner, repo), "--state", "open", "--limit", "100", "--json", "number"])
            .output()?;

        if !output.status.success() {
            anyhow::bail!("Failed to fetch PRs: {}", String::from_utf8_lossy(&output.stderr));
        }

        let prs: Vec<serde_json::Value> = serde_json::from_slice(&output.stdout)?;
        Ok(prs.iter().filter_map(|pr| pr["number"].as_u64().map(|n| n as u32)).collect())
    }

    fn extract_dependencies(body: &str) -> Vec<u32> {
        let patterns = [
            r"(?i)depends\s+on\s+#(\d+)",
            r"(?i)based\s+on\s+#(\d+)",
            r"(?i)stacked\s+on\s+#(\d+)",
            r"(?i)builds?\s+on\s+#(\d+)",
            r"(?i)requires?\s+#(\d+)",
            r"(?i)follows?\s+#(\d+)",
        ];

        let mut deps = Vec::new();
        for pattern in &patterns {
            let re = Regex::new(pattern).unwrap();
            for cap in re.captures_iter(body) {
                if let Ok(num) = cap[1].parse() {
                    deps.push(num);
                }
            }
        }
        deps
    }

    fn extract_stack_info(body: &str) -> Option<Vec<u32>> {
        let stack_re = Regex::new(r"(?i)stack:\s*\n((?:\s*-\s*#\d+[^\n]*\n?)*)").unwrap();
        let cap = stack_re.captures(body)?;
        let stack_section = cap.get(1)?.as_str();

        let pr_re = Regex::new(r"#(\d+)").unwrap();
        let mut stack_prs = Vec::new();
        for line in stack_section.lines() {
            if let Some(cap) = pr_re.captures(line) {
                if let Ok(num) = cap[1].parse() {
                    stack_prs.push(num);
                }
            }
        }

        if stack_prs.len() > 1 { Some(stack_prs) } else { None }
    }

    fn find_related_by_branch(&mut self, owner: &str, repo: &str, target: &PullRequest, all_prs: &[u32]) -> Vec<u32> {
        all_prs.iter()
            .filter(|&&num| num != target.number)
            .filter_map(|&num| self.fetch_pr(owner, repo, num).ok())
            .filter(|pr| pr.base_ref == target.head_ref || target.base_ref == pr.head_ref)
            .map(|pr| pr.number)
            .collect()
    }

    fn build_stack_graph(&mut self, owner: &str, repo: &str, start_pr: u32) -> Result<Vec<PullRequest>> {
        let start = self.fetch_pr(owner, repo, start_pr)?;

        if let Some(stack_prs) = Self::extract_stack_info(&start.body) {
            let mut stack: Vec<_> = stack_prs.iter()
                .filter_map(|&num| self.fetch_pr(owner, repo, num).ok())
                .collect();

            if let Ok(all_prs) = self.fetch_all_open_prs(owner, repo) {
                let mut visited: HashSet<_> = stack.iter().map(|pr| pr.number).collect();
                for pr in stack.clone() {
                    for related in self.find_related_by_branch(owner, repo, &pr, &all_prs) {
                        if visited.insert(related) {
                            if let Ok(pr) = self.fetch_pr(owner, repo, related) {
                                stack.push(pr);
                            }
                        }
                    }
                }
            }
            return Ok(stack);
        }

        let all_prs = self.fetch_all_open_prs(owner, repo)?;
        for &num in &all_prs {
            if num == start_pr { continue; }
            if let Ok(pr) = self.fetch_pr(owner, repo, num) {
                if let Some(stack_prs) = Self::extract_stack_info(&pr.body) {
                    if stack_prs.contains(&start_pr) {
                        return Ok(stack_prs.iter()
                            .filter_map(|&n| self.fetch_pr(owner, repo, n).ok())
                            .collect());
                    }
                }
            }
        }

        let mut visited = HashSet::new();
        let mut stack = Vec::new();
        self.traverse_branches(owner, repo, start_pr, &all_prs, &mut visited, &mut stack)?;

        if stack.len() > 1 {
            stack.sort_by_key(|pr| if pr.base_ref == "main" { 0 } else { 1 });
            return Ok(stack);
        }

        visited.clear();
        stack.clear();
        self.traverse_deps(owner, repo, start_pr, &all_prs, &mut visited, &mut stack)?;
        stack.sort_by_key(|pr| Self::extract_dependencies(&pr.body).len());
        Ok(stack)
    }

    fn traverse_branches(&mut self, owner: &str, repo: &str, num: u32, all_prs: &[u32], visited: &mut HashSet<u32>, stack: &mut Vec<PullRequest>) -> Result<()> {
        if !visited.insert(num) { return Ok(()); }
        let pr = self.fetch_pr(owner, repo, num)?;
        let related = self.find_related_by_branch(owner, repo, &pr, all_prs);
        stack.push(pr);
        for r in related {
            self.traverse_branches(owner, repo, r, all_prs, visited, stack)?;
        }
        Ok(())
    }

    fn traverse_deps(&mut self, owner: &str, repo: &str, num: u32, all_prs: &[u32], visited: &mut HashSet<u32>, stack: &mut Vec<PullRequest>) -> Result<()> {
        if !visited.insert(num) { return Ok(()); }
        let pr = self.fetch_pr(owner, repo, num)?;
        let deps = Self::extract_dependencies(&pr.body);
        stack.push(pr);
        for dep in deps {
            self.traverse_deps(owner, repo, dep, all_prs, visited, stack)?;
        }
        for &other in all_prs {
            if other == num { continue; }
            if let Ok(other_pr) = self.fetch_pr(owner, repo, other) {
                let other_deps = Self::extract_dependencies(&other_pr.body);
                if other_deps.contains(&num) {
                    self.traverse_deps(owner, repo, other, all_prs, visited, stack)?;
                }
            }
        }
        Ok(())
    }
}

fn print_stack(stack: &[PullRequest], current: u32) {
    println!("\nstack:");
    for pr in stack {
        let marker = if pr.number == current { " <-" } else { "" };
        println!("- #{} ({}): {}{}", pr.number, pr.state, pr.title, marker);
    }
    println!("--------");
    println!("\nstack:");
    for pr in stack {
        let marker = if pr.number == current { " <-" } else { "" };
        println!("- #{}{}", pr.number, marker);
    }
}

fn parse_github_url(url: &str) -> Result<(String, String, u32)> {
    let re = Regex::new(r"github\.com/([^/]+)/([^/]+)/pull/(\d+)").unwrap();
    let cap = re.captures(url).context("invalid GitHub PR URL format")?;
    Ok((cap[1].to_string(), cap[2].to_string(), cap[3].parse()?))
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    let (owner, repo, pr_number) = parse_github_url(&cli.pr_url)?;

    println!("Analyzing {}/{} #{}...", owner, repo, pr_number);

    let mut visualizer = StackVisualizer::new();
    let stack = visualizer.build_stack_graph(&owner, &repo, pr_number)?;
    print_stack(&stack, pr_number);

    Ok(())
}
