from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class EdgeType(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    EDGE_TYPE_UNSPECIFIED: _ClassVar[EdgeType]
    ENTITY_COOCCUR: _ClassVar[EdgeType]
    LLM_ASSERTED: _ClassVar[EdgeType]
    SPLIT_SIBLING: _ClassVar[EdgeType]
    REDIRECT: _ClassVar[EdgeType]
EDGE_TYPE_UNSPECIFIED: EdgeType
ENTITY_COOCCUR: EdgeType
LLM_ASSERTED: EdgeType
SPLIT_SIBLING: EdgeType
REDIRECT: EdgeType

class PutSegmentRequest(_message.Message):
    __slots__ = ("file_id", "content")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    content: bytes
    def __init__(self, file_id: _Optional[int] = ..., content: _Optional[bytes] = ...) -> None: ...

class PutSegmentResponse(_message.Message):
    __slots__ = ("file_id", "new_version")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    NEW_VERSION_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    new_version: int
    def __init__(self, file_id: _Optional[int] = ..., new_version: _Optional[int] = ...) -> None: ...

class GetFileRequest(_message.Message):
    __slots__ = ("file_id",)
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    def __init__(self, file_id: _Optional[int] = ...) -> None: ...

class GetFileResponse(_message.Message):
    __slots__ = ("content", "version")
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    VERSION_FIELD_NUMBER: _ClassVar[int]
    content: bytes
    version: int
    def __init__(self, content: _Optional[bytes] = ..., version: _Optional[int] = ...) -> None: ...

class ReadPartialRequest(_message.Message):
    __slots__ = ("file_id",)
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    def __init__(self, file_id: _Optional[int] = ...) -> None: ...

class HeaderOffset(_message.Message):
    __slots__ = ("header", "offset")
    HEADER_FIELD_NUMBER: _ClassVar[int]
    OFFSET_FIELD_NUMBER: _ClassVar[int]
    header: str
    offset: int
    def __init__(self, header: _Optional[str] = ..., offset: _Optional[int] = ...) -> None: ...

class ReadPartialResponse(_message.Message):
    __slots__ = ("headers",)
    HEADERS_FIELD_NUMBER: _ClassVar[int]
    headers: _containers.RepeatedCompositeFieldContainer[HeaderOffset]
    def __init__(self, headers: _Optional[_Iterable[_Union[HeaderOffset, _Mapping]]] = ...) -> None: ...

class GraphNeighborsRequest(_message.Message):
    __slots__ = ("file_id", "depth", "edge_type_filter", "max_nodes")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    EDGE_TYPE_FILTER_FIELD_NUMBER: _ClassVar[int]
    MAX_NODES_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    depth: int
    edge_type_filter: EdgeType
    max_nodes: int
    def __init__(self, file_id: _Optional[int] = ..., depth: _Optional[int] = ..., edge_type_filter: _Optional[_Union[EdgeType, str]] = ..., max_nodes: _Optional[int] = ...) -> None: ...

class Neighbor(_message.Message):
    __slots__ = ("target_file_id", "type", "weight", "hop")
    TARGET_FILE_ID_FIELD_NUMBER: _ClassVar[int]
    TYPE_FIELD_NUMBER: _ClassVar[int]
    WEIGHT_FIELD_NUMBER: _ClassVar[int]
    HOP_FIELD_NUMBER: _ClassVar[int]
    target_file_id: int
    type: EdgeType
    weight: int
    hop: int
    def __init__(self, target_file_id: _Optional[int] = ..., type: _Optional[_Union[EdgeType, str]] = ..., weight: _Optional[int] = ..., hop: _Optional[int] = ...) -> None: ...

class GraphNeighborsResponse(_message.Message):
    __slots__ = ("neighbors",)
    NEIGHBORS_FIELD_NUMBER: _ClassVar[int]
    neighbors: _containers.RepeatedCompositeFieldContainer[Neighbor]
    def __init__(self, neighbors: _Optional[_Iterable[_Union[Neighbor, _Mapping]]] = ...) -> None: ...

class SearchCandidatesRequest(_message.Message):
    __slots__ = ("query", "max_results")
    QUERY_FIELD_NUMBER: _ClassVar[int]
    MAX_RESULTS_FIELD_NUMBER: _ClassVar[int]
    query: str
    max_results: int
    def __init__(self, query: _Optional[str] = ..., max_results: _Optional[int] = ...) -> None: ...

class CandidateTopic(_message.Message):
    __slots__ = ("file_id", "path", "score")
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    SCORE_FIELD_NUMBER: _ClassVar[int]
    file_id: int
    path: str
    score: float
    def __init__(self, file_id: _Optional[int] = ..., path: _Optional[str] = ..., score: _Optional[float] = ...) -> None: ...

class SearchCandidatesResponse(_message.Message):
    __slots__ = ("candidates",)
    CANDIDATES_FIELD_NUMBER: _ClassVar[int]
    candidates: _containers.RepeatedCompositeFieldContainer[CandidateTopic]
    def __init__(self, candidates: _Optional[_Iterable[_Union[CandidateTopic, _Mapping]]] = ...) -> None: ...

class ProposeSplitRequest(_message.Message):
    __slots__ = ("file_content",)
    FILE_CONTENT_FIELD_NUMBER: _ClassVar[int]
    file_content: bytes
    def __init__(self, file_content: _Optional[bytes] = ...) -> None: ...

class SectionRange(_message.Message):
    __slots__ = ("start", "end")
    START_FIELD_NUMBER: _ClassVar[int]
    END_FIELD_NUMBER: _ClassVar[int]
    start: int
    end: int
    def __init__(self, start: _Optional[int] = ..., end: _Optional[int] = ...) -> None: ...

class SplitFileProposal(_message.Message):
    __slots__ = ("new_path", "section_ranges")
    NEW_PATH_FIELD_NUMBER: _ClassVar[int]
    SECTION_RANGES_FIELD_NUMBER: _ClassVar[int]
    new_path: str
    section_ranges: _containers.RepeatedCompositeFieldContainer[SectionRange]
    def __init__(self, new_path: _Optional[str] = ..., section_ranges: _Optional[_Iterable[_Union[SectionRange, _Mapping]]] = ...) -> None: ...

class ProposeSplitResponse(_message.Message):
    __slots__ = ("files", "redirect_summary")
    FILES_FIELD_NUMBER: _ClassVar[int]
    REDIRECT_SUMMARY_FIELD_NUMBER: _ClassVar[int]
    files: _containers.RepeatedCompositeFieldContainer[SplitFileProposal]
    redirect_summary: str
    def __init__(self, files: _Optional[_Iterable[_Union[SplitFileProposal, _Mapping]]] = ..., redirect_summary: _Optional[str] = ...) -> None: ...

class PutEdgeRequest(_message.Message):
    __slots__ = ("source_file_id", "target_file_id", "edge_type", "weight")
    SOURCE_FILE_ID_FIELD_NUMBER: _ClassVar[int]
    TARGET_FILE_ID_FIELD_NUMBER: _ClassVar[int]
    EDGE_TYPE_FIELD_NUMBER: _ClassVar[int]
    WEIGHT_FIELD_NUMBER: _ClassVar[int]
    source_file_id: int
    target_file_id: int
    edge_type: EdgeType
    weight: int
    def __init__(self, source_file_id: _Optional[int] = ..., target_file_id: _Optional[int] = ..., edge_type: _Optional[_Union[EdgeType, str]] = ..., weight: _Optional[int] = ...) -> None: ...

class PutEdgeResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class PutEntityRequest(_message.Message):
    __slots__ = ("entity_name", "file_id")
    ENTITY_NAME_FIELD_NUMBER: _ClassVar[int]
    FILE_ID_FIELD_NUMBER: _ClassVar[int]
    entity_name: str
    file_id: int
    def __init__(self, entity_name: _Optional[str] = ..., file_id: _Optional[int] = ...) -> None: ...

class PutEntityResponse(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class LookupEntityRequest(_message.Message):
    __slots__ = ("entity_name",)
    ENTITY_NAME_FIELD_NUMBER: _ClassVar[int]
    entity_name: str
    def __init__(self, entity_name: _Optional[str] = ...) -> None: ...

class LookupEntityResponse(_message.Message):
    __slots__ = ("file_ids",)
    FILE_IDS_FIELD_NUMBER: _ClassVar[int]
    file_ids: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, file_ids: _Optional[_Iterable[int]] = ...) -> None: ...
