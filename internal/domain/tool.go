package domain

type Tool struct {
	Name        string
	Description string
	Parameters  []ToolParameter
}

type ToolParameter struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

func DefaultTools() []Tool {
	return []Tool{
		{
			Name:        "execute_powershell",
			Description: "원격 Windows 머신에서 PowerShell 스크립트를 작성하고 실행합니다",
			Parameters: []ToolParameter{
				{Name: "script", Type: "string", Description: "실행할 PowerShell 스크립트", Required: true},
			},
		},
		{
			Name:        "read_file",
			Description: "원격 머신의 파일 내용을 읽습니다",
			Parameters: []ToolParameter{
				{Name: "path", Type: "string", Description: "읽을 파일 경로", Required: true},
			},
		},
		{
			Name:        "write_file",
			Description: "원격 머신의 파일에 내용을 씁니다",
			Parameters: []ToolParameter{
				{Name: "path", Type: "string", Description: "쓸 파일 경로", Required: true},
				{Name: "content", Type: "string", Description: "파일에 쓸 내용", Required: true},
			},
		},
		{
			Name:        "list_directory",
			Description: "원격 머신의 디렉토리 목록을 조회합니다",
			Parameters: []ToolParameter{
				{Name: "path", Type: "string", Description: "조회할 디렉토리 경로", Required: true},
			},
		},
		{
			Name:        "get_system_info",
			Description: "원격 머신의 시스템 정보(CPU, 메모리, OS)를 조회합니다",
			Parameters:  []ToolParameter{},
		},
	}
}
