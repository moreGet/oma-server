-- 로컬 개발용 샘플 LLM 제공자 데이터
INSERT IGNORE INTO llm_providers (id, name, is_active, provider_type, config_json) VALUES
    (1, 'Ollama-local', 1, 'LOCAL',    '{"endpoint":"http://localhost:11434","model":"llama3"}'),
    (2, 'Claude-3',     0, 'EXTERNAL', '{"model":"claude-3-5-sonnet-20241022","api_key_env":"ANTHROPIC_API_KEY"}'),
    (3, 'GPT-4o',       0, 'EXTERNAL', '{"model":"gpt-4o","api_key_env":"OPENAI_API_KEY"}');
