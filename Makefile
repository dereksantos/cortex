.PHONY: help install install-dev test test-cov lint format type-check clean build publish docs docs-serve

help:  ## Show this help message
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

install:  ## Install the package
	pip install -e .

install-dev:  ## Install development dependencies
	pip install -e ".[dev]"
	pre-commit install

test:  ## Run tests
	pytest

test-cov:  ## Run tests with coverage
	pytest --cov=context_capture --cov-report=html --cov-report=term

lint:  ## Run linting
	flake8 context_capture tests
	mypy context_capture

format:  ## Format code
	black context_capture tests
	isort context_capture tests

type-check:  ## Run type checking
	mypy context_capture

clean:  ## Clean build artifacts
	rm -rf build/
	rm -rf dist/
	rm -rf *.egg-info/
	rm -rf .pytest_cache/
	rm -rf .coverage
	rm -rf htmlcov/
	find . -type d -name __pycache__ -exec rm -rf {} +
	find . -type f -name "*.pyc" -delete

build:  ## Build the package
	python -m build

publish:  ## Publish to PyPI
	python -m twine upload dist/*

docs:  ## Build documentation
	mkdocs build

docs-serve:  ## Serve documentation locally
	mkdocs serve

# Development workflow shortcuts
dev-setup: install-dev  ## Complete development setup
	@echo "Development environment ready!"

quick-test: format lint test  ## Quick development test cycle

release-check: clean build  ## Check release readiness
	python -m twine check dist/*