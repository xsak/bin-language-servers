// Package protocol holds the subset of LSP 3.17 types that bls uses.
//
// Only fields we read or write are declared; unknown fields from the client are ignored.
package protocol

import "encoding/json"

// DocumentURI is a file URI such as file:///home/me/x.yaml.
type DocumentURI string

// Position encodings (LSP 3.17 general.positionEncodings).
const (
	PositionEncodingUTF8  = "utf-8"
	PositionEncodingUTF16 = "utf-16"
	PositionEncodingUTF32 = "utf-32"
)

// Position is zero-based; Character counts code units of the negotiated encoding.
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   DocumentURI `json:"uri"`
	Range Range       `json:"range"`
}

type TextDocumentIdentifier struct {
	URI DocumentURI `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     DocumentURI `json:"uri"`
	Version int32       `json:"version"`
}

type TextDocumentItem struct {
	URI        DocumentURI `json:"uri"`
	LanguageID string      `json:"languageId"`
	Version    int32       `json:"version"`
	Text       string      `json:"text"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// --- lifecycle ---

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type WorkspaceFolder struct {
	URI  DocumentURI `json:"uri"`
	Name string      `json:"name"`
}

type ClientCapabilities struct {
	General *struct {
		PositionEncodings []string `json:"positionEncodings,omitempty"`
	} `json:"general,omitempty"`
	Workspace *struct {
		Configuration bool `json:"configuration,omitempty"`
	} `json:"workspace,omitempty"`
	TextDocument *struct {
		Completion *struct {
			CompletionItem *struct {
				SnippetSupport bool `json:"snippetSupport,omitempty"`
			} `json:"completionItem,omitempty"`
		} `json:"completion,omitempty"`
		Hover *struct {
			ContentFormat []string `json:"contentFormat,omitempty"`
		} `json:"hover,omitempty"`
	} `json:"textDocument,omitempty"`
}

type InitializeParams struct {
	ProcessID             *int32             `json:"processId"`
	ClientInfo            *ClientInfo        `json:"clientInfo,omitempty"`
	RootURI               DocumentURI        `json:"rootUri,omitempty"`
	InitializationOptions json.RawMessage    `json:"initializationOptions,omitempty"`
	Capabilities          ClientCapabilities `json:"capabilities"`
	WorkspaceFolders      []WorkspaceFolder  `json:"workspaceFolders,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

// TextDocumentSyncKind values.
const (
	SyncNone        = 0
	SyncFull        = 1
	SyncIncremental = 2
)

type SaveOptions struct {
	IncludeText bool `json:"includeText"`
}

type TextDocumentSyncOptions struct {
	OpenClose bool         `json:"openClose"`
	Change    int          `json:"change"`
	Save      *SaveOptions `json:"save,omitempty"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
	ResolveProvider   bool     `json:"resolveProvider,omitempty"`
}

type ServerCapabilities struct {
	PositionEncoding           string                   `json:"positionEncoding,omitempty"`
	TextDocumentSync           *TextDocumentSyncOptions `json:"textDocumentSync,omitempty"`
	CompletionProvider         *CompletionOptions       `json:"completionProvider,omitempty"`
	HoverProvider              bool                     `json:"hoverProvider,omitempty"`
	DefinitionProvider         bool                     `json:"definitionProvider,omitempty"`
	ReferencesProvider         bool                     `json:"referencesProvider,omitempty"`
	DocumentSymbolProvider     bool                     `json:"documentSymbolProvider,omitempty"`
	DocumentFormattingProvider bool                     `json:"documentFormattingProvider,omitempty"`
	LinkedEditingRangeProvider bool                     `json:"linkedEditingRangeProvider,omitempty"`
}

// --- document sync ---

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

// TextDocumentContentChangeEvent replaces Range with Text; a nil Range replaces the whole document.
type TextDocumentContentChangeEvent struct {
	Range *Range `json:"range,omitempty"`
	Text  string `json:"text"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         *string                `json:"text,omitempty"`
}

type DidChangeConfigurationParams struct {
	Settings json.RawMessage `json:"settings"`
}

// --- diagnostics ---

type DiagnosticSeverity int

const (
	SeverityError       DiagnosticSeverity = 1
	SeverityWarning     DiagnosticSeverity = 2
	SeverityInformation DiagnosticSeverity = 3
	SeverityHint        DiagnosticSeverity = 4
)

type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Code     any                `json:"code,omitempty"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         DocumentURI  `json:"uri"`
	Version     *int32       `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// --- language features ---

type CompletionParams struct {
	TextDocumentPositionParams
}

type CompletionItemKind int

const (
	CompletionKindText     CompletionItemKind = 1
	CompletionKindMethod   CompletionItemKind = 2
	CompletionKindFunction CompletionItemKind = 3
	CompletionKindField    CompletionItemKind = 5
	CompletionKindVariable CompletionItemKind = 6
	CompletionKindModule   CompletionItemKind = 9
	CompletionKindProperty CompletionItemKind = 10
	CompletionKindValue    CompletionItemKind = 12
	CompletionKindEnum     CompletionItemKind = 13
	CompletionKindKeyword  CompletionItemKind = 14
	CompletionKindSnippet  CompletionItemKind = 15
	CompletionKindFile     CompletionItemKind = 17
	CompletionKindConstant CompletionItemKind = 21
)

type InsertTextFormat int

const (
	InsertTextPlain   InsertTextFormat = 1
	InsertTextSnippet InsertTextFormat = 2
)

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type CompletionItem struct {
	Label            string             `json:"label"`
	Kind             CompletionItemKind `json:"kind,omitempty"`
	Detail           string             `json:"detail,omitempty"`
	Documentation    *MarkupContent     `json:"documentation,omitempty"`
	SortText         string             `json:"sortText,omitempty"`
	FilterText       string             `json:"filterText,omitempty"`
	InsertText       string             `json:"insertText,omitempty"`
	InsertTextFormat InsertTextFormat   `json:"insertTextFormat,omitempty"`
	TextEdit         *TextEdit          `json:"textEdit,omitempty"`
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

const (
	MarkupPlainText = "plaintext"
	MarkupMarkdown  = "markdown"
)

type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type HoverParams struct {
	TextDocumentPositionParams
}

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type DefinitionParams struct {
	TextDocumentPositionParams
}

type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type SymbolKind int

const (
	SymbolFile      SymbolKind = 1
	SymbolModule    SymbolKind = 2
	SymbolNamespace SymbolKind = 3
	SymbolClass     SymbolKind = 5
	SymbolMethod    SymbolKind = 6
	SymbolProperty  SymbolKind = 7
	SymbolField     SymbolKind = 8
	SymbolFunction  SymbolKind = 12
	SymbolVariable  SymbolKind = 13
	SymbolConstant  SymbolKind = 14
	SymbolString    SymbolKind = 15
	SymbolNumber    SymbolKind = 16
	SymbolBoolean   SymbolKind = 17
	SymbolArray     SymbolKind = 18
	SymbolObject    SymbolKind = 19
	SymbolKey       SymbolKind = 20
	SymbolNull      SymbolKind = 21
)

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

type FormattingOptions struct {
	TabSize                uint32 `json:"tabSize"`
	InsertSpaces           bool   `json:"insertSpaces"`
	TrimTrailingWhitespace bool   `json:"trimTrailingWhitespace,omitempty"`
	InsertFinalNewline     bool   `json:"insertFinalNewline,omitempty"`
	TrimFinalNewlines      bool   `json:"trimFinalNewlines,omitempty"`
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Options      FormattingOptions      `json:"options"`
}

// --- window ---

type MessageType int

const (
	MessageError   MessageType = 1
	MessageWarning MessageType = 2
	MessageInfo    MessageType = 3
	MessageLog     MessageType = 4
)

type LogMessageParams struct {
	Type    MessageType `json:"type"`
	Message string      `json:"message"`
}
