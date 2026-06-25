@echo off
curl.exe -v -X POST "https://token-plan-cn.xiaomimimo.com/v1/chat/completions" -H "Content-Type: application/json" -H "Authorization: Bearer tp-crk53oqlw7ey3lukvrqtjj9auv3w6ssiyakbky6pkc5nl8f4" -d "{\"model\":\"mimo-v2.5-pro\",\"max_tokens\":16,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"
