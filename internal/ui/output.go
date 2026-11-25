package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OutputMode 定义命令输出模式（美观表格 / JSON / 无边框 TSV）
type OutputMode string

const (
	OutputPretty OutputMode = "pretty"
	OutputJSON   OutputMode = "json"
	OutputPlain  OutputMode = "plain"
)

// ResolveOutputMode 根据命令标志解析最终输出模式
func ResolveOutputMode(jsonFlag, plainFlag bool) OutputMode {
	if jsonFlag {
		return OutputJSON
	}
	if plainFlag {
		return OutputPlain
	}
	return OutputPretty
}

// WriteJSON 将结构化数据以缩进清晰的 JSON 输出到目标流
func WriteJSON(w io.Writer, v interface{}) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteTSV 将二维行数据以无转义码、无表头的制表符分隔格式输出
func WriteTSV(w io.Writer, rows [][]string) error {
	for _, row := range rows {
		cleaned := make([]string, len(row))
		for i, cell := range row {
			c := strings.ReplaceAll(cell, "\t", " ")
			c = strings.ReplaceAll(c, "\r", "")
			c = strings.ReplaceAll(c, "\n", " ")
			cleaned[i] = c
		}
		if _, err := fmt.Fprintln(w, strings.Join(cleaned, "\t")); err != nil {
			return err
		}
	}
	return nil
}
