use alm_herald::http::ApiDoc;
use utoipa::OpenApi;

fn main() {
    let doc = ApiDoc::openapi();
    let format = std::env::args().nth(1).unwrap_or_default();
    match format.as_str() {
        "json" => print!("{}", doc.to_json().expect("json serialize")),
        _ => print!("{}", serde_yaml::to_string(&doc).expect("yaml serialize")),
    }
}
