format:
	gofumpt -l -w .

redis:
	docker run -it --rm \
		-p 6379:6379 \
		redis
