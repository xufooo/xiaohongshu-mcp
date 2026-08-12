package xiaohongshu

// 小红书 Feed 相关的数据结构定义

// FeedResponse 表示从 __INITIAL_STATE__ 中获取的完整 Feed 响应
type FeedResponse struct {
	Feed FeedData `json:"feed"`
}

// FeedData 表示 feed 数据结构
type FeedData struct {
	Feeds FeedsValue `json:"feeds"`
}

// FeedsValue 表示 feeds 的值结构
type FeedsValue struct {
	Value []Feed `json:"_value"`
}

// Feed 表示单个 Feed 项目
type Feed struct {
	XsecToken string   `json:"xsecToken"`
	ID        string   `json:"id"`
	ModelType string   `json:"modelType"`
	NoteCard  NoteCard `json:"noteCard"`
	Index     int      `json:"index"`
}

// AIChatReply 表示搜索页生成的 AI 回复。
type AIChatReply struct {
	Content string `json:"content,omitempty"`
	HasMore bool   `json:"has_more"`
}

// SearchPageResult 聚合搜索页的笔记和 AI 回复。
type SearchPageResult struct {
	Feeds  []Feed       `json:"feeds"`
	AIChat *AIChatReply `json:"ai_chat,omitempty"`

	DebugSearchTotalMS           int64   `json:"debug_search_total_ms"`
	DebugSearchPrecheckMS        int64   `json:"debug_search_precheck_ms"`
	DebugSearchInputMS           int64   `json:"debug_search_input_ms"`
	DebugSearchSubmitMS          int64   `json:"debug_search_submit_ms"`
	DebugSearchWaitMS            int64   `json:"debug_search_wait_ms"`
	DebugSearchExtractMS         int64   `json:"debug_search_extract_ms"`
	DebugSearchInputProbeMs      []int64 `json:"debug_search_input_probe_ms"`
	DebugSearchInputProbeCount   int     `json:"debug_search_input_probe_count"`
	DebugSearchInputProbeFailed  int     `json:"debug_search_input_probe_failed"`
	DebugSearchInputLastErrorKind string  `json:"debug_search_input_last_error_kind"`
	DebugSearchResultProbeMs     []int64 `json:"debug_search_result_probe_ms"`
	DebugSearchResultProbeCount  int     `json:"debug_search_result_probe_count"`
	DebugSearchResultProbeFailed int     `json:"debug_search_result_probe_failed"`
	DebugSearchResultLastErrorKind string `json:"debug_search_result_last_error_kind"`
	DebugSearchWaitExit          string  `json:"debug_search_wait_exit"`
	DebugSearchFallback          bool    `json:"debug_search_fallback"`
	DebugSearchWaitRounds        int     `json:"debug_search_wait_rounds"`
}

// NoteCard 表示笔记卡片信息
type NoteCard struct {
	Type         string       `json:"type"`
	DisplayTitle string       `json:"displayTitle"`
	User         User         `json:"user"`
	InteractInfo InteractInfo `json:"interactInfo"`
	Cover        Cover        `json:"cover"`
	Video        *Video       `json:"video,omitempty"` // 视频内容，可能为空
}

// User 表示用户信息
type User struct {
	UserID    string `json:"userId"`
	Nickname  string `json:"nickname"`
	NickName  string `json:"nickName"`
	Avatar    string `json:"avatar"`
	XsecToken string `json:"xsecToken"` // 用户主页 xsec_token，供 user_profile 使用
}

// InteractInfo 表示互动信息
type InteractInfo struct {
	Liked      bool   `json:"liked"`
	LikedCount string `json:"likedCount"`

	SharedCount  string `json:"sharedCount"`
	CommentCount string `json:"commentCount"`

	CollectedCount string `json:"collectedCount"`
	Collected      bool   `json:"collected"`
}

// Cover 表示封面信息
type Cover struct {
	Width      int         `json:"width"`
	Height     int         `json:"height"`
	URL        string      `json:"url"`
	FileID     string      `json:"fileId"`
	URLPre     string      `json:"urlPre"`
	URLDefault string      `json:"urlDefault"`
	InfoList   []ImageInfo `json:"infoList"`
}

// ImageInfo 表示图片信息
type ImageInfo struct {
	ImageScene string `json:"imageScene"`
	URL        string `json:"url"`
}

// Video 表示视频信息
type Video struct {
	Capa VideoCapability `json:"capa"`
}

// VideoCapability 表示视频能力信息
type VideoCapability struct {
	Duration int `json:"duration"` // 视频时长，单位秒
}

// ================ Feed 详情页相关结构体 ================

// FeedDetailResponse 表示 Feed 详情页完整响应
type FeedDetailResponse struct {
	Note     FeedDetail  `json:"note"`
	Comments CommentList `json:"comments"`
}

// FeedDetail 表示详情页的笔记内容
type FeedDetail struct {
	NoteID       string            `json:"noteId"`
	XsecToken    string            `json:"xsecToken"`
	Title        string            `json:"title"`
	Desc         string            `json:"desc"`
	Type         string            `json:"type"`
	Time         int64             `json:"time"`
	IPLocation   string            `json:"ipLocation"`
	User         User              `json:"user"`
	InteractInfo InteractInfo      `json:"interactInfo"`
	ImageList    []DetailImageInfo `json:"imageList"` // 图文笔记的具体图片 URL，视频笔记不填
}

// OpenedNoteContent 是打开笔记时读取的首屏内容：正文、作者、互动数据。
// 图片 URL 随 open_note 返回，是 agent 后续调用 vision 理解图片的直接输入。
type OpenedNoteContent struct {
	NoteID       string            `json:"note_id"`
	Title        string            `json:"title"`
	Desc         string            `json:"desc"`
	Type         string            `json:"type"`
	User         User              `json:"user"`
	InteractInfo InteractInfo      `json:"interactInfo"`
	ImageList    []DetailImageInfo `json:"imageList"`
}

// SessionDetailResponse 是打开后继续读取评论的结果；不承担图片读取。
type SessionDetailResponse struct {
	NoteID   string    `json:"note_id"`
	Comments []Comment `json:"comments"`
}

// DetailImageInfo 表示详情页的图片信息
type DetailImageInfo struct {
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	URLDefault string `json:"urlDefault"`
	URLPre     string `json:"urlPre"`
	LivePhoto  bool   `json:"livePhoto,omitempty"`
}

// CommentList 表示评论列表
type CommentList struct {
	List             []Comment `json:"list"`
	Cursor           string    `json:"cursor"`
	HasMore          bool      `json:"hasMore"`
	TotalItems       int       `json:"totalItems,omitempty"`
	SeenCount        int       `json:"seenCount"`
	Complete         bool      `json:"complete"`
	IncompleteReason string    `json:"incomplete_reason,omitempty"`
}

// Comment 表示单条评论
type Comment struct {
	ID              string    `json:"id"`
	NoteID          string    `json:"noteId"`
	Content         string    `json:"content"`
	LikeCount       string    `json:"likeCount"`
	CreateTime      int64     `json:"createTime"`
	IPLocation      string    `json:"ipLocation"`
	Liked           bool      `json:"liked"`
	UserInfo        User      `json:"userInfo"`
	SubCommentCount string    `json:"subCommentCount"`
	SubComments     []Comment `json:"subComments"`
	ShowTags        []string  `json:"showTags"`
}

// UserProfileResponse 用户详情页完整响应
type UserProfileResponse struct {
	UserBasicInfo UserBasicInfo      `json:"userBasicInfo"`
	Interactions  []UserInteractions `json:"interactions"`
	Feeds         []Feed             `json:"feeds"`
}

// UserPageData 用户的详细信息
type UserPageData struct {
	RawValue struct {
		Interactions []UserInteractions `json:"interactions"`
		BasicInfo    UserBasicInfo      `json:"basicInfo"`
	} `json:"_rawValue"`
}

// UserBasicInfo 用户的基本信息
type UserBasicInfo struct {
	Gender     int    `json:"gender"`
	IpLocation string `json:"ipLocation"`
	Desc       string `json:"desc"`
	Imageb     string `json:"imageb"`
	Nickname   string `json:"nickname"`
	Images     string `json:"images"`
	RedId      string `json:"redId"`
}

// UserInteractions 用户的 关注 粉丝 收藏量
type UserInteractions struct {
	Type  string `json:"type"`  // follows fans interaction
	Name  string `json:"name"`  // 关注 粉丝 获赞与收藏
	Count string `json:"count"` // 数量
}
